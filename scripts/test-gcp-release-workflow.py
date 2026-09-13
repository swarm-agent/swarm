#!/usr/bin/env python3
"""Requirement: build-main promotes only exact GCP bytes, never silently uses SLSA.
Threat: custom predicate confusion or unsigned evidence accepted at publication.
Authority: verify-release-evidence.sh and build-main.yml. Execute its embedded
predicate verifier with hermetic fixtures; no credentials or crypto simulation
is treated as proof of Sigstore trust. Static workflow checks are wiring only.
"""
import copy
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('fixtures', ROOT / 'scripts/test-gcp-result-consumers.py')
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)


class PromotionTests(unittest.TestCase):
    def test_signed_predicate_and_tampering(self):
        script = (ROOT / 'scripts/verify-release-evidence.sh').read_text()
        program = script.split("<<'PY'\n", 1)[1].split('\nPY\n', 1)[0]
        for change in ('none', 'type', 'signed', 'evidence', 'input', 'archive', 'source'):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as tmp:
                d = Path(tmp)
                _, manifest, evidence, _, inputs = f.ReleaseTests().evidence()
                root = 'swarm-v1.2.3-linux-amd64'
                info = ''.join(k + '=' + v + '\n' for k, v in dict(version=inputs['version'], commit=inputs['source_sha'], actor=inputs['actor'], ref=inputs['ref'], built_at=inputs['built_at']).items()).encode()
                buffer = io.BytesIO()
                with tarfile.open(fileobj=buffer, mode='w:gz') as tar:
                    for name, raw in {root + '/build-info.txt': info, root + '/install.sh': b'x', root + '/linux-amd64/root/swarm': b'x', root + '/linux-amd64/swarmd/swarmd': b'x'}.items():
                        member = tarfile.TarInfo(name)
                        member.mode, member.size = 0o644, len(raw)
                        tar.addfile(member, io.BytesIO(raw))
                raw = buffer.getvalue()
                digest = f.s.digest(raw)
                manifest['archive_sha256'] = digest
                manifest['archive']['sha256'] = digest
                binding = evidence['receipt']['binding']
                binding['artifacts']['archive']['sha256'] = digest
                manifest['evidence_binding'] = copy.deepcopy(binding)
                original = {k: copy.deepcopy(binding[k]) for k in ('repository_id', 'pull_number', 'controller_id', 'run_id', 'source_tree', 'execution_tree', 'build_inputs')}
                original['event'] = evidence['admission']['event']
                token = dict(evidence['admission']['jobs']['build'], run_id=binding['run_id'], binding_digest=f.s.hashed(original))
                build = dict(token=token, status='passed', build_id=binding['build_id'],
                             outputs={k: copy.deepcopy(binding['artifacts'][k]) for k in ('archive', 'checksum')},
                             members=[dict(stage=name, status='passed', timing={'startTime': '2026-01-01T00:00:00Z', 'endTime': '2026-01-01T00:00:01Z'}) for name in f.s.BUILD_STAGES])
                provenance = dict(schema='swarm.runtime-build-proof/v1', run_id=binding['run_id'], binding=original,
                                  result=build, authority=evidence['admission']['authority'])
                manifest['provenance']['sha256'] = f.s.hashed(provenance)
                binding['artifacts']['provenance']['sha256'] = f.s.hashed(provenance)
                manifest['evidence_binding'] = copy.deepcopy(binding)
                predicate = dict(schema='swarm.release-promotion/v1', builder='gcp', promoter='github-actions', source_sha=f.A, archive_sha256=digest, gcp_build_id=binding['build_id'], gcp_run_id=binding['run_id'], github_run_id='1', qualification_evidence_sha256=f.s.digest(f.s.canonical(evidence)))
                statement = dict(predicateType='https://swarm.dev/attestations/release-promotion/v1', predicate=copy.deepcopy(predicate))
                if change == 'type': statement['predicateType'] = 'https://slsa.dev/provenance/v1'
                if change == 'signed': statement['predicate']['builder'] = 'github-actions'
                if change == 'evidence': evidence['receipt']['cleanup_verified'] = False
                if change == 'input': binding['build_input_digest'] = '0' * 64
                if change == 'source': predicate['source_sha'] = f.B
                if change == 'archive': raw += b'altered'
                archive = d / (root + '.tar.gz')
                archive.write_bytes(raw)
                (d / 'build-info.txt').write_bytes(info)
                for name, value in {'promotion-predicate.json': predicate, 'gcp-qualification-evidence.json': evidence, 'gcp-release-manifest.json': manifest, 'gcp-build-provenance.json': provenance, 'verified.json': [{'verificationResult': {'statement': statement}}]}.items():
                    (d / name).write_bytes(f.s.canonical(value))
                before = {p.name: p.read_bytes() for p in d.iterdir()}
                result = subprocess.run([sys.executable, '-B', '-', str(d / 'verified.json'), str(d), str(archive), f.A], input=program, text=True, capture_output=True, cwd=ROOT, timeout=10)
                self.assertEqual(result.returncode == 0, change == 'none', result.stderr[-1000:])
                self.assertEqual(before, {p.name: p.read_bytes() for p in d.iterdir()})

    def test_workflow_contract(self):
        workflow = (ROOT / '.github/workflows/build-main.yml').read_text()
        for forbidden in ('setup-go@', 'setup-node@', 'apt-get', 'pnpm install', 'build-main-dist.sh', 'smoke-release-archive.sh', 'run-critical-tests.sh', 'smoke-evidence.txt'):
            self.assertNotIn(forbidden, workflow)
        for required in ('build-stable-release:', 'publish-stable-release:', 'environment: stable-release', 'predicate-type: https://swarm.dev/attestations/release-promotion/v1', 'scripts/gcp-release-input.py'):
            self.assertIn(required, workflow)
        self.assertEqual(workflow.count('--gcp-promotion-dir '), 2)

    def test_no_implicit_legacy_mode(self):
        result = subprocess.run(['bash', 'scripts/verify-release-evidence.sh', 'absent', 'absent', 'absent', 'absent'], cwd=ROOT, capture_output=True, text=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('select exactly one provenance contract', result.stderr)


if __name__ == '__main__':
    unittest.main()
