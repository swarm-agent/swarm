#!/usr/bin/env python3
"""Requirement: the storage guard must allow the reviewed private-turn file rename
without exempting arbitrary renames, migrations, or home/temp storage defaults.
Authority: check-daemon-storage-paths.sh filter_allowed and
ArtifactV3AuthorService.Rename in runtime_artifact_v3_author.go. Execute the real
shell guard against isolated source fixtures: this is the narrowest layer that
proves classification and unchanged source, not filesystem containment itself.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
AUTHOR = 'swarmd/internal/tool/runtime_artifact_v3_author.go'
RENAME = ('\tif err = os.Rename(filepath.Join(state.root, filepath.FromSlash(source)), '
          'filepath.Join(state.root, filepath.FromSlash(destination))); err == nil {')


class ArtifactRenameStorageGuardTests(unittest.TestCase):
    def scan(self, files, accepted, reason=None):
        with tempfile.TemporaryDirectory(dir=os.environ['TMPDIR']) as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            guard = root / 'scripts/check-daemon-storage-paths.sh'
            shutil.copyfile(ROOT / 'scripts/check-daemon-storage-paths.sh', guard)
            for name, content in files.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content)
            before = {p.relative_to(root): p.read_bytes()
                      for p in root.rglob('*') if p.is_file()}
            result = subprocess.run(['bash', str(guard)], text=True,
                                    capture_output=True, timeout=15)
            self.assertEqual(result.returncode, 0 if accepted else 1,
                             result.stdout + result.stderr)
            self.assertIn('[storage-path-check] PASS' if accepted else
                          '[storage-path-check] FAIL:', result.stdout)
            if reason:
                self.assertIn(reason, result.stdout)
            after = {p.relative_to(root): p.read_bytes()
                     for p in root.rglob('*') if p.is_file()}
            self.assertEqual(before, after, 'storage guard must not modify source')

    def test_current_author_and_exact_call_pass(self):
        source = (ROOT / AUTHOR).read_text()
        self.assertIn(RENAME + '\n', source)
        self.scan({AUTHOR: source}, True)
        self.scan({AUTHOR: RENAME + '\n'}, True)

    def test_other_renames_and_migrations_remain_rejected(self):
        cases = [
            (AUTHOR, RENAME.replace('state.root', 'legacyRoot', 1)),
            (AUTHOR, RENAME.replace('FromSlash(destination)', 'FromSlash(other)')),
            (AUTHOR, RENAME + ' os.Rename(old, new)'),
            (AUTHOR, RENAME + '\n\tos.Rename(old, new)'),
            (AUTHOR, RENAME + '\n\tmigrateLegacyStorage()'),
            (AUTHOR, RENAME + '\n\tcopyDir(old, new)'),
            (AUTHOR + '.extra.go', RENAME),
            ('swarmd/internal/tool/unrelated.go', RENAME),
            ('swarmd/internal/tool/nested/runtime_artifact_v3_author.go', RENAME),
        ]
        for name, text in cases:
            with self.subTest(name=name, text=text):
                self.scan({name: text + '\n'}, False, 'potential storage migration')

    def test_other_storage_checks_still_scan_author(self):
        for suffix, reason in [
            ('home, _ := os.UserHomeDir()', 'home/XDG/user roots'),
            ('root := os.TempDir()', 'OS temp'),
            ('root := "/workspace/data"', 'workspace'),
        ]:
            with self.subTest(suffix=suffix):
                self.scan({AUTHOR: RENAME + '\n' + suffix + '\n'}, False, reason)


if __name__ == '__main__':
    unittest.main()
