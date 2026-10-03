"""Requirement: publication uses precisely the reviewed SDK/CLI packs.
Threat: glob-injected third package, modified bytes or swapped package identity.
Authority: verify-npm-release-packs.verify. Hermetic tar fixtures prove this
pre-publication boundary without executing npm or publishing anything.
"""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('packs', Path(__file__).resolve().parents[2] / 'scripts/verify-npm-release-packs.py')
packs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(packs)


class PackTests(unittest.TestCase):
    def test_exact_list_and_reject_substitution(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            rows = []
            for name in ('sdk', 'cli'):
                raw = json.dumps(dict(name='@swarm-agent/'+name, version='1.2.3')).encode()
                path = root / ('swarm-agent-'+name+'-1.2.3.tgz')
                with tarfile.open(path, 'w:gz') as archive:
                    member = tarfile.TarInfo('package/package.json')
                    member.size = len(raw)
                    archive.addfile(member, io.BytesIO(raw))
                rows.append(dict(name='@swarm-agent/'+name, version='1.2.3', file=path.name,
                                 sha256=hashlib.sha256(path.read_bytes()).hexdigest()))
            (root/'npm-packages.json').write_text(json.dumps(rows))
            self.assertEqual(packs.verify(root, 'v1.2.3'), [str(root/r['file']) for r in rows])
            extra = root/'injected.tgz'
            extra.write_bytes(b'injected')
            with self.assertRaises(ValueError):
                packs.verify(root, 'v1.2.3')
            extra.unlink()
            original = (root/rows[0]['file']).read_bytes()
            (root/rows[0]['file']).write_bytes(original+b'tamper')
            with self.assertRaises(ValueError):
                packs.verify(root, 'v1.2.3')
            (root/rows[0]['file']).write_bytes(original)
            rows[0]['name'] = '@swarm-agent/cli'
            (root/'npm-packages.json').write_text(json.dumps(rows))
            with self.assertRaises(ValueError):
                packs.verify(root, 'v1.2.3')


if __name__ == '__main__':
    unittest.main()
