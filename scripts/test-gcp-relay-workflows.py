#!/usr/bin/env python3
"""Requirement: preserve Actions contexts while only trusted relay code runs locally.
Threat: candidate checkout, skipped matrix checks, or local compute bypass GCP.
Authority: five workflow job/step definitions and gcp-result-relay.poll.
YAML structure is the narrowest wiring test, NOT proof of authentication or live
Actions identity. Fake API failures exercise the actual relay without networking.
Requires PyYAML; parent must execute and reconcile the test audit ledger/atlas.
"""
import importlib.util
import json
from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
JOBS = {
    'critical-tests': ['atlas-driven-critical-tests'],
    'install-distro-smoke': ['build-candidate', 'distro-install'],
    'dependency-vulnerability-scan': ['scan-dependencies'],
    'require-changelog': ['require-changelog'],
    'guard-main-pr-source': ['require-dev-head'],
}
CHECKOUT = 'actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09'
TRUSTED = '${{ vars.GCP_VERIFIER_SHA || github.event.pull_request.base.sha || github.workflow_sha }}'
spec = importlib.util.spec_from_file_location('relay', ROOT / 'scripts/gcp-result-relay.py')
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)


class WorkflowTests(unittest.TestCase):
    def test_preserved_events_contexts_and_only_trusted_relay(self):
        for name, ids in JOBS.items():
            with self.subTest(workflow=name):
                # BaseLoader avoids YAML 1.1 interpreting the Actions key `on` as True.
                doc = yaml.load((ROOT / '.github/workflows' / (name + '.yml')).read_text(), Loader=yaml.BaseLoader)
                self.assertEqual(doc['name'], name)
                if name in ('require-changelog', 'guard-main-pr-source'):
                    types = ['opened', 'reopened', 'synchronize']
                    if name == 'guard-main-pr-source':
                        types.append('edited')
                    types.append('ready_for_review')
                    events = {'pull_request': {'branches': ['main'], 'types': types}}
                else:
                    events = {'pull_request': {'branches': ['dev', 'main']},
                              'push': {'branches': ['dev']}, 'workflow_dispatch': ''}
                    if name == 'dependency-vulnerability-scan':
                        events['push']['branches'].append('main')
                        events['schedule'] = [{'cron': '23 5 * * 1'}]
                self.assertEqual(doc['on'], events)
                self.assertEqual(doc['permissions'], dict.fromkeys(['contents', 'checks', 'pull-requests', 'actions'], 'read'))
                self.assertEqual(list(doc['jobs']), ids)
                for job in doc['jobs'].values():
                    self.assertEqual(set(job) - {'strategy'}, {'runs-on', 'timeout-minutes', 'steps'})
                    self.assertEqual(job['runs-on'], 'ubuntu-latest')
                    self.assertEqual(job['timeout-minutes'], '45')
                    checkout, run = job['steps']
                    self.assertEqual(set(checkout), {'name', 'uses', 'with'})
                    self.assertEqual(checkout['uses'], CHECKOUT)
                    self.assertEqual(checkout['with'], {
                        'ref': TRUSTED, 'persist-credentials': 'false',
                        'sparse-checkout': 'scripts/gcp-result-relay.py',
                        'sparse-checkout-cone-mode': 'false'})
                    self.assertEqual(set(run), {'name', 'env', 'run'})
                    self.assertTrue(run['run'].endswith('python3 -I scripts/gcp-result-relay.py\n'))
                    self.assertIn('[[ "${GCP_VERIFIER_SHA}" =~ ^[0-9a-f]{40}$ ]]', run['run'])
                    self.assertIn('[[ "$(git rev-parse HEAD)" == "${GCP_VERIFIER_SHA}" ]]', run['run'])
                    self.assertEqual(run['env'], {
                        'GITHUB_TOKEN': '${{ github.token }}',
                        'GCP_VERIFIER_SHA': '${{ vars.GCP_VERIFIER_SHA }}',
                        'GCP_CHECK_APP_ID': '${{ vars.GCP_CHECK_APP_ID }}',
                        'GCP_CHECK_CONTEXT': name})
                if name == 'install-distro-smoke':
                    self.assertEqual(doc['jobs']['distro-install']['strategy'], {
                        'fail-fast': 'false', 'matrix': {'distro': ['ubuntu', 'arch']}})

    def test_shell_preflight_rejects_missing_config_and_preserves_relay_failure(self):
        # Execute the exact workflow shell without network. A successful preflight
        # must still propagate a failed qualification verifier, never turn green.
        import os
        import subprocess
        import tempfile
        for name in JOBS:
            doc = yaml.load((ROOT / '.github/workflows' / (name + '.yml')).read_text(), Loader=yaml.BaseLoader)
            for job in doc['jobs'].values():
                program = job['steps'][1]['run']
                for app, present, code, message in (
                        ('', False, 1, 'GCP_CHECK_APP_ID repository variable'),
                        ('invalid', True, 1, 'GCP_CHECK_APP_ID repository variable'),
                        ('42', False, 1, 'Trusted checkout lacks the relay'),
                        ('42', True, 7, 'awaiting actual qualification')):
                    with self.subTest(workflow=name, app=app, present=present), tempfile.TemporaryDirectory() as tmp:
                        root = Path(tmp)
                        (root / 'scripts').mkdir()
                        if present:
                            (root / 'scripts/gcp-result-relay.py').write_text('raise SystemExit(7)\n')
                        env = dict(PATH=os.environ['PATH'], GCP_CHECK_APP_ID=app, GCP_VERIFIER_SHA='')
                        result = subprocess.run(['bash', '-c', program], cwd=root, env=env,
                            text=True, capture_output=True, timeout=5)
                        self.assertEqual(result.returncode, code, result.stderr)
                        self.assertIn(message, result.stdout)

    def test_each_context_rejects_missing_app_before_network(self):
        for context in JOBS:
            with self.subTest(context=context):
                def forbidden(*args):
                    self.fail('missing App configuration must not reach API')
                with self.assertRaises(relay.Invalid):
                    relay.poll({'GITHUB_REPOSITORY': 'example/project',
                                'GCP_CHECK_CONTEXT': context, 'GCP_CHECK_APP_ID': ''}, {}, forbidden)

    def test_each_context_propagates_failed_stage_despite_green_check(self):
        # Full poll boundary: no success return when any required stage fails.
        head, base, tree = 'a' * 40, 'b' * 40, 'c' * 40
        for context in JOBS:
            with self.subTest(context=context):
                env = dict(GITHUB_REPOSITORY='example/project', GITHUB_TOKEN='fake',
                           GITHUB_SHA=head, GITHUB_REF='refs/heads/dev',
                           GITHUB_EVENT_NAME='push', GCP_CHECK_APP_ID='42',
                           GCP_CHECK_CONTEXT=context)
                event = dict(repository={'id': 7}, before=base, after=head, ref=env['GITHUB_REF'])
                result = dict(schema='swarm.gcp.check-result/v1', repository_id=7,
                              event_name='push', pull_number=0, phase='execution', before_sha=base, head_sha=head,
                              base_sha=base, execution_sha=head, source_tree=tree,
                              execution_tree=tree, input_digest='d' * 64,
                              run_id='run-1', context=context, state='passed',
                              cleanup_verified=True,
                              stages=[{'id': x, 'status': 'passed'} for x in sorted(relay.STAGES[context])])
                result['stages'][0]['status'] = 'failed'
                check = dict(id=10, app={'id': 42}, name=context, head_sha=head,
                             status='completed', conclusion='success', output={'text': json.dumps(result)})
                def transport(url, headers):
                    if '/check-runs?' in url:
                        value = {'total_count': 1, 'check_runs': [check]}
                    elif '/git/ref/' in url:
                        value = {'object': {'sha': head}}
                    elif '/git/commits/' in url:
                        value = {'tree': {'sha': tree}, 'parents': [{'sha': base}]}
                    else:
                        value = {'id': 7}
                    return json.dumps(value).encode()
                with self.assertRaisesRegex(relay.Invalid, 'qualification failed'):
                    relay.poll(env, event, transport, sleep=lambda _: self.fail('terminal failure must not poll again'))


if __name__ == '__main__':
    unittest.main()
