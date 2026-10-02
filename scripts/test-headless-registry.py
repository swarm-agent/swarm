#!/usr/bin/env python3
"""Purpose: probe-headless-registry.probe is the read-only publication gate.
Only authenticated explicit absence may permit a write; denied, malformed and
network failures must not masquerade as first publish. Fake transport is the
narrowest deterministic layer; this is not live GHCR qualification.
"""
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('probe', Path(__file__).with_name('probe-headless-registry.py'))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)
MANIFEST = b'{"schemaVersion":2}'
DIGEST = 'sha256:' + hashlib.sha256(MANIFEST).hexdigest()


class ProbeTests(unittest.TestCase):
    def run_probe(self, status, body, auth=(200, b'{"token":"test-bearer"}')):
        calls = []
        def request(url, headers):
            calls.append(url)
            self.assertTrue(url.startswith('https://ghcr.io/'))
            if len(calls) == 1:
                self.assertTrue(headers['Authorization'].startswith('Basic '))
                return auth
            self.assertEqual(headers['Authorization'], 'Bearer test-bearer')
            self.assertIn('/manifests/v1.2.3', url)
            return status, body
        return probe.probe('v1.2.3', DIGEST, 'actor', 'test-secret', request)

    def test_only_explicit_authenticated_absence(self):
        for code in ('NAME_UNKNOWN', 'MANIFEST_UNKNOWN'):
            self.assertEqual(self.run_probe(404, json.dumps({'errors': [{'code': code}]}).encode()), 'absent')
        for status, body in [(403, b'{}'), (401, b'{}'), (500, b'{}'),
                             (404, b'not json'), (404, b'{}'), (404, b'{"errors":[]}'),
                             (404, b'{"errors":[{"code":"DENIED"}]}'),
                             (404, b'{"errors":[{"code":"NAME_UNKNOWN"},{"code":"DENIED"}]}'),
                             (302, b'{}')]:
            with self.subTest(status=status, body=body), self.assertRaises(probe.Invalid):
                self.run_probe(status, body)

    def test_existing_tag_must_match_exact_bytes(self):
        self.assertEqual(self.run_probe(200, MANIFEST), 'present')
        with self.assertRaises(probe.Invalid):
            self.run_probe(200, MANIFEST + b' ')

    def test_authentication_and_network_fail_closed(self):
        for auth in [(401, b'{}'), (404, b'{"errors":[{"code":"NAME_UNKNOWN"}]}'),
                     (200, b'{}'), (200, b'null'), (200, b'{"token":""}')]:
            with self.subTest(auth=auth), self.assertRaises(probe.Invalid):
                self.run_probe(404, b'{"errors":[{"code":"NAME_UNKNOWN"}]}', auth)
        def offline(*args):
            raise OSError('network unavailable')
        with self.assertRaises(OSError):
            probe.probe('v1.2.3', DIGEST, 'actor', 'test-secret', offline)
        with self.assertRaises(probe.Invalid):
            probe.probe('v1.2.3', DIGEST, 'actor', '', offline)

    def test_redirects_are_not_followed(self):
        with self.assertRaises(probe.Invalid):
            probe.NoRedirect().redirect_request(None, None, 302, '', {}, 'https://example.invalid/')


if __name__ == '__main__':
    unittest.main()
