"""Requirement: exact source/version OCI bytes alone may be pinned and promoted.
Threats: digest substitution, unsafe archives, foreign platform, native evidence
masquerading as image proof, and PR/dispatch publication. Production authorities:
headless-release.inspect/verify/publication_gate and gcp-release-input.consume_image.
Hermetic data-level tests are the narrowest proof of rejection; these fixtures are
not runtime qualification, provider telemetry, or evidence of registry access.
"""
import copy
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


h = load('image_test', 'headless-release.py')
r = load('consumer_test', 'gcp-release-input.py')
SOURCE = 'a' * 40


def fixture(arch='amd64', extra=None):
    files = {'oci-layout': b'{"imageLayoutVersion":"1.0.0"}'}

    def blob(value, media):
        raw = json.dumps(value).encode()
        digest = h.sha(raw)
        files['blobs/sha256/' + digest] = raw
        return dict(mediaType=media, size=len(raw), digest='sha256:' + digest)

    config = blob(dict(os='linux', architecture=arch, config=dict(User='10001:10001', Entrypoint=['/usr/local/bin/swarmd', '--desktop-port=0', '--cwd=/project'], Labels={
        'org.opencontainers.image.source': 'https://github.com/swarm-agent/swarm',
        'org.opencontainers.image.revision': SOURCE,
        'org.opencontainers.image.version': 'v1.2.3'})), 'application/vnd.oci.image.config.v1+json')
    layer = blob('unit-test layer bytes', 'application/vnd.oci.image.layer.v1.tar')
    manifest = blob(dict(schemaVersion=2, config=config, layers=[layer]), 'application/vnd.oci.image.manifest.v1+json')
    files['index.json'] = json.dumps(dict(schemaVersion=2, manifests=[manifest])).encode()
    if extra:
        files.update(extra)
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode='w') as tar:
        for name, raw in files.items():
            member = tarfile.TarInfo(name)
            member.size = len(raw)
            tar.addfile(member, io.BytesIO(raw))
    return buffer.getvalue()


class HeadlessReleaseTests(unittest.TestCase):
    def test_exact_pin_and_rejections(self):
        raw = fixture()
        metadata = h.inspect(raw, 'v1.2.3', SOURCE)
        self.assertEqual(h.verify(raw, metadata, 'v1.2.3', SOURCE), metadata)
        self.assertEqual(h.pin(metadata)['reference'], h.IMAGE + '@' + metadata['manifestDigest'])
        self.assertEqual(h.pin(metadata)['version'], '1.2.3')
        for key in ('archiveSha256', 'manifestDigest', 'imageId', 'sourceCommit', 'platform', 'version'):
            changed = dict(metadata, **{key: 'wrong'})
            with self.subTest(key=key), self.assertRaises(ValueError):
                h.verify(raw, changed, 'v1.2.3', SOURCE)
        for data, version, source in [(fixture('arm64'), 'v1.2.3', SOURCE),
                                      (raw, 'v1.2.4', SOURCE), (raw, 'v1.2.3', 'b' * 40),
                                      (fixture(extra={'../escape': b'x'}), 'v1.2.3', SOURCE),
                                      (fixture(extra={'blobs/sha256/' + '0' * 64: b'x', 'index.json': b'{}'}), 'v1.2.3', SOURCE)]:
            with self.subTest(version=version, source=source), self.assertRaises((ValueError, KeyError)):
                h.inspect(data, version, source)

    def test_publication_boundary(self):
        env = dict(GITHUB_REPOSITORY='swarm-agent/swarm', GITHUB_EVENT_NAME='push',
                   GITHUB_REF='refs/heads/main', RELEASE_ENVIRONMENT='stable-release')
        h.publication_gate(env)
        for key, value in [('GITHUB_REPOSITORY', 'fork/swarm'), ('GITHUB_EVENT_NAME', 'pull_request'),
                           ('GITHUB_EVENT_NAME', 'workflow_dispatch'), ('GITHUB_REF', 'refs/heads/dev'),
                           ('RELEASE_ENVIRONMENT', '')]:
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                h.publication_gate(dict(env, **{key: value}))

    def test_consumer_requires_bound_proof_and_separate_qualification(self):
        raw = fixture()
        meta = h.inspect(raw, 'v1.2.3', SOURCE)
        metadata_raw = json.dumps(meta).encode()
        objects = {'oci_archive': raw, 'oci_metadata': metadata_raw}
        artifacts = {k: dict(bucket='packages', object=k, generation='1', sha256=h.sha(v)) for k, v in objects.items()}
        manifest = {k: dict(v, bucket='qualified') for k, v in artifacts.items()}
        evidence = dict(receipt=dict(binding=dict(artifacts=artifacts, run_id='test-run'), headless=dict(
            schema='swarm.headless-qualification/v1', source_sha=SOURCE, run_id='test-run', image=meta,
            gates={k: 'passed' for k in ('startup', 'scoped-auth', 'sdk-session', 'restart')})))
        proof = dict(result=dict(outputs=copy.deepcopy(artifacts)))
        policy = dict(package_bucket='packages', qualified_bucket='qualified')

        class Store:
            def get(self, ref, **kwargs):
                return objects[ref['object']]

        self.assertEqual(r.consume_image(Store(), manifest, evidence, proof, policy, 'v1.2.3', SOURCE),
                         {'swarm-headless.oci.tar': raw, 'headless-image.json': metadata_raw})
        for change in ('missing', 'bucket', 'digest', 'generation', 'proof', 'qualification'):
            m, e, p = copy.deepcopy((manifest, evidence, proof))
            if change == 'missing': del m['oci_archive']
            if change == 'bucket': m['oci_archive']['bucket'] = 'untrusted'
            if change == 'digest': m['oci_archive']['sha256'] = '0' * 64
            if change == 'generation': del m['oci_archive']['generation']
            if change == 'proof': p['result']['outputs']['oci_archive']['sha256'] = '0' * 64
            if change == 'qualification': e['receipt']['headless']['gates']['sdk-session'] = 'failed'
            before = copy.deepcopy((m, e, p))
            with self.subTest(change=change), self.assertRaises((r.relay.Invalid, KeyError)):
                r.consume_image(Store(), m, e, p, policy, 'v1.2.3', SOURCE)
            self.assertEqual((m, e, p), before)


if __name__ == '__main__':
    unittest.main()
