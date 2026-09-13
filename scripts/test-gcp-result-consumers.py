#!/usr/bin/env python3
"""Hermetic requirement-first consumer tests; no network/cloud/checkout execution.

Authority: relay.GitHub/verify_check/poll, release.Google/verify_evidence/archive_info.
Threat: an authenticated but stale/incomplete result or altered release must not
become a green relay or locally staged signing input. Function/transport fakes are
the narrowest boundary; they do not establish live producer/schema compatibility.
"""
import copy
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import unittest


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


r = load('relay_test', 'gcp-result-relay.py')
s = load('release_test', 'gcp-release-input.py')
A, B, T = 'a' * 40, 'b' * 40, 'c' * 40


def fixture():
    expected = dict(repository_id=7, event_name='push', before_sha=B,
                    head_sha=A, base_sha=B, execution_sha=A, source_tree=T, execution_tree=T)
    result = dict(expected, schema='swarm.gcp.check-result/v1', input_digest='d' * 64,
                  run_id='run-1', context='critical-tests', state='success', cleanup_verified=True,
                  stages=[{'id': x, 'status': 'passed'} for x in sorted(r.STAGES['critical-tests'])])
    check = dict(id=10, app={'id': 42}, name='critical-tests', head_sha=A,
                 status='completed', conclusion='success', output={'text': json.dumps(result)})
    return expected, result, check


class RelayTests(unittest.TestCase):
    def test_completed_exact_app_and_coverage(self):
        expected, result, check = fixture()
        self.assertEqual(r.verify_check(check, expected, 'critical-tests', 42, r.STAGES['critical-tests']), (result, True))

    def test_forged_stale_and_incomplete_results_fail_closed(self):
        # No producer booleans, skipped stages, wrong App or base can authorize success.
        for change in ('app', 'base', 'missing', 'duplicate', 'skip', 'cleanup', 'unknown', 'size', 'failure'):
            with self.subTest(change=change):
                expected, result, check = fixture()
                if change == 'app': check['app']['id'] = 43
                if change == 'base': result['base_sha'] = A
                if change == 'missing': result['stages'].pop()
                if change == 'duplicate': result['stages'][-1] = result['stages'][0]
                if change == 'skip': result['stages'][0]['status'] = 'skipped'
                if change == 'cleanup': result['cleanup_verified'] = False
                if change == 'unknown': result['candidate_verified'] = True
                if change == 'failure': check['conclusion'] = 'failure'
                check['output']['text'] = json.dumps(result) if change != 'size' else ' ' * 65537
                with self.assertRaises(r.Invalid):
                    r.verify_check(check, expected, 'critical-tests', 42, r.STAGES['critical-tests'])

    def test_dispatch_cannot_reuse_old_invocation(self):
        expected, result, check = fixture()
        expected.update(logical_run='github-99', invocation_created_at='2026-01-01T00:00:00Z')
        with self.assertRaises(r.Invalid):
            r.verify_check(check, expected, 'critical-tests', 42, r.STAGES['critical-tests'])

    def test_poll_uses_fixed_api_and_rejects_duplicate_checks(self):
        _, _, check = fixture()
        env = dict(GITHUB_TOKEN='fake', GITHUB_REPOSITORY='example/project', GITHUB_SHA=A,
                   GITHUB_REF='refs/heads/main', GITHUB_EVENT_NAME='push',
                   GCP_CHECK_APP_ID='42', GCP_CHECK_CONTEXT='critical-tests')
        event = dict(repository={'id': 7}, before=B, after=A, ref=env['GITHUB_REF'])
        calls = []
        def transport(url, headers):
            calls.append(url)
            self.assertTrue(url.startswith('https://api.github.com/repos/example/project'))
            if '/check-runs?' in url: value = {'total_count': 2, 'check_runs': [check, check]}
            elif '/git/ref/' in url: value = {'object': {'sha': A}}
            elif '/git/commits/' in url: value = {'tree': {'sha': T}, 'parents': [{'sha': B}]}
            else: value = {'id': 7}
            return json.dumps(value).encode()
        with self.assertRaisesRegex(r.Invalid, 'ambiguous producer'):
            r.poll(env, event, transport)
        self.assertLess(len(calls), 12)

    def test_duplicate_json_and_redirects_rejected(self):
        with self.assertRaises(r.Invalid): r.decode('{"ok":true,"ok":false}')
        with self.assertRaises(r.Invalid):
            r.NoRedirect().redirect_request(None, None, 302, '', {}, 'https://evil.invalid')


class ReleaseTests(unittest.TestCase):
    def evidence(self):
        expected, result, _ = fixture()
        inputs = dict(source_sha=A, tree_sha=T, build_spec_sha256='1' * 64,
                      locks_sha256='2' * 64, toolchains_sha256='3' * 64, version='v1.2.3',
                      built_at='2026-01-01T00:00:00Z', actor='automation', ref='refs/heads/main',
                      trust_realm='trusted', harness_sha=B)
        result['input_digest'] = s.digest(s.canonical({'schema': 'swarm.gcp.workload/v2', **inputs}))
        refs = {k: {'bucket': 'example-release', 'object': k, 'generation': '1', 'sha256': 'a' * 64} for k in ('archive', 'checksum', 'provenance')}
        binding = dict(repository_id=7, pull_number=0, source_sha=A, comparison_base_sha=B,
                       execution_sha=A, execution_tree=T, source_tree=T, trust_profile='trusted',
                       build_inputs=inputs, build_input_digest=result['input_digest'], artifacts=refs,
                       controller_id='controller', build_id='build-1', run_id='run-1')
        receipt = dict(binding=binding, stages=result['stages'], onboarding=[{'id': 'first-run', 'status': 'passed'}],
                       jobs=[{'id': 'job', 'attempt': 2, 'status': 'passed'}], cleanup={'verified': True, 'remaining_resources': []})
        evidence = dict(schema='swarm.gcp.release-evidence/v1', receipt=receipt,
                        admission={'binding': copy.deepcopy(binding)}, observed_authority={'binding': copy.deepcopy(binding)})
        manifest = dict(schema='swarm.release-handoff/v1', qualification='passed', cleanup_verified=True,
                        source_sha=A, build_id='build-1', run_id='run-1', evidence_binding=copy.deepcopy(binding), **refs)
        policy = dict(schema='swarm.gcp.release-policy/v1', stages=[x['id'] for x in result['stages']],
                      onboarding=['first-run'], current_attempts={'job': 2},
                      binding={k: copy.deepcopy(v) for k, v in binding.items() if k not in ('artifacts', 'build_id', 'run_id')})
        return result, manifest, evidence, policy, inputs

    def test_full_independent_binding(self):
        result, manifest, evidence, policy, inputs = self.evidence()
        self.assertEqual(s.verify_evidence(result, manifest, evidence, policy, 'v1.2.3'), inputs)

    def test_missing_stale_attempt_base_source_version_cleanup_rejected(self):
        for change in ('onboarding', 'attempt', 'base', 'source', 'version', 'cleanup', 'authority', 'artifact'):
            with self.subTest(change=change):
                result, manifest, evidence, policy, _ = self.evidence()
                if change == 'onboarding': evidence['receipt']['onboarding'] = []
                if change == 'attempt': evidence['receipt']['jobs'][0]['attempt'] = 1
                if change == 'base': result['base_sha'] = A
                if change == 'source': manifest['source_sha'] = B
                if change == 'cleanup': evidence['receipt']['cleanup']['remaining_resources'] = ['vm']
                if change == 'authority': evidence['observed_authority']['binding']['controller_id'] = 'other'
                if change == 'artifact': manifest['archive'] = {}
                with self.assertRaises(s.relay.Invalid):
                    s.verify_evidence(result, manifest, evidence, policy, 'v1.2.4' if change == 'version' else 'v1.2.3')

    def test_gcs_exact_generation_digest_and_tampering(self):
        google = object.__new__(s.Google)
        google.buckets, google.token = ['example-release'], 'fake'
        calls = []
        def transport(url, headers, limit):
            calls.append(url)
            return b'original'
        google.transport = transport
        ref = dict(bucket='example-release', object='path/archive', generation='8', sha256=s.digest(b'original'))
        self.assertEqual(google.get(ref), b'original')
        self.assertIn('/path%2Farchive?alt=media&generation=8', calls[0])
        ref['sha256'] = s.digest(b'altered')
        with self.assertRaises(s.relay.Invalid): google.get(ref)
        ref['bucket'] = 'unapproved'
        with self.assertRaises(s.relay.Invalid): google.get(ref)
        self.assertEqual(len(calls), 2)

    def test_oidc_target_fails_before_credentials_leave(self):
        env = dict(GCP_WORKLOAD_IDENTITY_PROVIDER='projects/123/locations/global/workloadIdentityPools/pool/providers/provider',
                   GCP_READ_SERVICE_ACCOUNT='reader@example-project.iam.gserviceaccount.com',
                   GCP_ALLOWED_BUCKETS='["example-release"]',
                   ACTIONS_ID_TOKEN_REQUEST_URL='https://evil.invalid/token', ACTIONS_ID_TOKEN_REQUEST_TOKEN='fake')
        calls = []
        with self.assertRaises(s.relay.Invalid):
            s.Google(env, lambda *args, **kwargs: calls.append(args))
        self.assertEqual(calls, [])

    def test_archive_metadata_and_traversal_without_extraction(self):
        _, _, _, _, inputs = self.evidence()
        root = 'swarm-v1.2.3-linux-amd64'
        info = ''.join(k + '=' + v + '\n' for k, v in dict(version='v1.2.3', commit=A, actor=inputs['actor'], ref=inputs['ref'], built_at=inputs['built_at']).items()).encode()
        def archive(extra=None):
            buffer = io.BytesIO()
            with tarfile.open(fileobj=buffer, mode='w:gz') as tar:
                files = {root + '/build-info.txt': info, root + '/install.sh': b'x', root + '/linux-amd64/root/swarm': b'x', root + '/linux-amd64/swarmd/swarmd': b'x'}
                if extra: files[extra] = b'x'
                for name, raw in files.items():
                    member = tarfile.TarInfo(name)
                    member.mode, member.size = 0o644, len(raw)
                    tar.addfile(member, io.BytesIO(raw))
            return buffer.getvalue()
        self.assertEqual(s.archive_info(archive(), 'v1.2.3', inputs), info)
        with self.assertRaises(s.relay.Invalid): s.archive_info(archive(root + '/../escape'), 'v1.2.3', inputs)
        inputs['source_sha'] = B
        with self.assertRaises(s.relay.Invalid): s.archive_info(archive(), 'v1.2.3', inputs)


if __name__ == '__main__':
    unittest.main()
