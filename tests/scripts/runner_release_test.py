"""Requirement: only App-bound exact-main native AND image receipts may be promoted.
Threats: stale source, cross-bucket references, missing/failed gates, tampered bytes
and post-download substitution. Authorities: gcp-runner-release verify_bytes,
consume and reverify; relay.verify_check authenticates App identity. This hermetic
contract layer tests rejection without network/publication; fixtures are not live
qualification evidence or benchmark measurements.
"""
import copy
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from headless_release_test import r, h, fixture, SOURCE

runner = r.runner
VERSION = 'v1.2.3'


def inputs():
    objects = {}
    bucket = 'qualified-test'
    def put(name, raw):
        reference = dict(bucket=bucket, object='candidate-test/' + name, generation='1', sha256=r.digest(raw))
        objects[reference['object']] = raw
        return reference
    native = dict(source_sha=SOURCE, version=VERSION, ref='detached', actor='test-builder', built_at='2026-01-01T00:00:00Z')
    info = ('version=v1.2.3\ncommit=' + SOURCE + '\nref=detached\nactor=test-builder\nbuilt_at=2026-01-01T00:00:00Z\n').encode()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w:gz') as tar:
        for name, raw in [('build-info.txt', info), ('install.sh', b'test'), ('linux-amd64/root/swarm', b'test'), ('linux-amd64/swarmd/swarmd', b'test')]:
            member = tarfile.TarInfo('swarm-v1.2.3-linux-amd64/' + name)
            member.size, member.mode = len(raw), 0o644
            tar.addfile(member, io.BytesIO(raw))
    archive = buf.getvalue()
    oci = fixture()
    meta = h.inspect(oci, VERSION, SOURCE)
    files = dict(archive=archive, checksum=(r.digest(archive)+'  swarm-v1.2.3-linux-amd64.tar.gz\n').encode(), oci_archive=oci, oci_metadata=r.canonical(meta))
    artifacts = {k: put(k, raw) for k, raw in files.items()}
    identity = dict(version=VERSION, source_sha=SOURCE, source_tree='b'*40, repository_id=12, run_id='test-run', build_id='test-build', native=native)
    receipt = dict(schema=runner.RECEIPT, **identity, artifacts=artifacts, cleanup_verified=True,
                   native_exit_codes={k: 0 for k in runner.NATIVE}, headless=dict(image=meta, exit_codes={k: 0 for k in runner.HEADLESS}))
    eraw = r.canonical(receipt)
    evidence = put('receipt.json', eraw)
    manifest = dict(schema=runner.SCHEMA, **identity, **artifacts, evidence=evidence)
    mraw = r.canonical(manifest)
    handoff = dict(manifest=put('manifest.json', mraw), verification=evidence)
    result = dict(schema='swarm.gcp.check-result/v1', context='build-main', repository_id=12,
                  event_name='push', pull_number=0, head_sha=SOURCE, execution_sha=SOURCE,
                  source_tree='b'*40, execution_tree='b'*40, state='passed', phase='execution',
                  cleanup_verified=True, run_id='test-run', input_digest='c'*64,
                  stages=[dict(id=k, status='passed') for k in sorted(runner.NATIVE)], release=handoff)
    policy = dict(schema=runner.POLICY, qualified_bucket=bucket)
    return result, mraw, eraw, policy, files, objects


class RunnerReleaseTests(unittest.TestCase):
    def test_roundtrip_consume_and_signed_reverification(self):
        result, mraw, eraw, policy, files, objects = inputs()
        class Store:
            def get(self, ref, **kwargs):
                return objects[ref['object']]
        with tempfile.TemporaryDirectory() as tmp:
            env = dict(GITHUB_EVENT_NAME='push', GITHUB_REF='refs/heads/main', GITHUB_SHA=SOURCE,
                       GITHUB_REPOSITORY='test/repo', GCP_CHECK_CONTEXT='build-main', GCP_RELEASE_VERSION=VERSION,
                       GITHUB_RUN_ID='7', GCP_OUTPUT_DIR=tmp)
            with patch.object(r.relay, 'poll', return_value=result) as poll, patch.object(r.relay.GitHub, 'identity', return_value={'head_sha': SOURCE}):
                r.consume(env, {}, policy, Store())
            self.assertEqual(poll.call_args.kwargs['required_stages'], runner.NATIVE)
            directory = Path(tmp)
            predicate = r.decode((directory/'promotion-predicate.json').read_bytes())
            self.assertEqual(runner.reverify(r, directory, SOURCE, predicate), 'swarm-v1.2.3-linux-amd64.tar.gz')
            (directory/'headless-image.json').write_bytes(b'{}')
            with self.assertRaises(ValueError):
                runner.reverify(r, directory, SOURCE, predicate)

    def test_tamper_and_failure_rejections_leave_inputs_unchanged(self):
        for case in ('source', 'tree', 'version', 'bucket', 'generation', 'missing-native', 'failed-native', 'boolean-exit', 'missing-image', 'failed-image', 'wrong-image', 'receipt-digest', 'manifest-digest', 'archive-bytes', 'unknown-schema', 'artifact-ref'):
            result, mraw, eraw, policy, files, _ = inputs()
            m, e = r.decode(mraw), r.decode(eraw)
            if case == 'source': m['source_sha'] = 'd'*40
            if case == 'tree': m['source_tree'] = 'd'*40
            if case == 'version': m['version'] = 'v1.2.4'
            if case == 'bucket': m['archive']['bucket'] = 'untrusted'
            if case == 'generation': m['archive']['generation'] = '0'
            if case == 'missing-native': del e['native_exit_codes']['critical-deep']
            if case == 'failed-native': e['native_exit_codes']['critical-fast'] = 1
            if case == 'boolean-exit': e['native_exit_codes']['critical-fast'] = False
            if case == 'missing-image': del e['headless']
            if case == 'failed-image': e['headless']['exit_codes']['restart'] = 1
            if case == 'wrong-image': e['headless']['image']['imageId'] = 'sha256:'+'0'*64
            if case == 'artifact-ref': e['artifacts']['archive']['generation'] = '2'
            if case == 'unknown-schema': m['schema'] = 'unknown'
            eraw = r.canonical(e)
            m['evidence']['sha256'] = r.digest(eraw)
            result['release']['verification'] = copy.deepcopy(m['evidence'])
            mraw = r.canonical(m)
            result['release']['manifest']['sha256'] = r.digest(mraw)
            if case == 'receipt-digest': result['release']['verification']['sha256'] = '0'*64
            if case == 'manifest-digest': result['release']['manifest']['sha256'] = '0'*64
            if case == 'archive-bytes': files['archive'] += b'tamper'
            before = copy.deepcopy((result, policy, files))
            with self.subTest(case=case), self.assertRaises((ValueError, KeyError)):
                runner.verify_bytes(r, result, mraw, eraw, policy, VERSION, SOURCE, files)
            self.assertEqual((result, policy, files), before)

    def test_no_output_on_failed_qualification(self):
        result, mraw, eraw, policy, files, objects = inputs()
        result['release']['manifest']['bucket'] = 'untrusted'
        with tempfile.TemporaryDirectory() as tmp:
            env = dict(GITHUB_EVENT_NAME='push', GITHUB_REF='refs/heads/main', GITHUB_SHA=SOURCE,
                       GITHUB_REPOSITORY='test/repo', GCP_CHECK_CONTEXT='build-main', GCP_RELEASE_VERSION=VERSION,
                       GITHUB_RUN_ID='7', GCP_OUTPUT_DIR=tmp)
            with patch.object(r.relay, 'poll', return_value=result), self.assertRaises(ValueError):
                r.consume(env, {}, policy, object())
            self.assertEqual(list(Path(tmp).iterdir()), [])

    def test_wrong_app_and_stale_head(self):
        result, *_ = inputs()
        expected = {k: result[k] for k in ('head_sha', 'execution_sha', 'source_tree', 'repository_id')}
        check = dict(app={'id': 9}, head_sha=SOURCE, output={'text': json.dumps(result)}, status='completed', conclusion='success')
        self.assertTrue(r.relay.verify_check(check, expected, 'build-main', 9, runner.NATIVE)[1])
        for field in ('app', 'head_sha'):
            changed = copy.deepcopy(check)
            changed[field] = {'id': 10} if field == 'app' else 'f'*40
            with self.assertRaises(ValueError):
                r.relay.verify_check(changed, expected, 'build-main', 9, runner.NATIVE)


if __name__ == '__main__':
    unittest.main()
