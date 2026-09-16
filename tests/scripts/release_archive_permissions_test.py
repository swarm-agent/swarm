#!/usr/bin/env python3
"""Purpose: public archive admission must reject inaccessible/private build modes.
Authority: verify-release-candidate.sh before extraction/installer execution;
build-main-dist.sh tar serialization. Small real archives prove rejection before
installer side effects, not actual runtime health. No network or privileges.
"""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ArchivePermissions(unittest.TestCase):
    def test_modes_and_numeric_ownership_before_installer(self):
        with tempfile.TemporaryDirectory(dir=os.environ['TMPDIR']) as tmp:
            base = Path(tmp)
            for defect in ('none', 'private-directory', 'private-binary', 'writable', 'special', 'owner'):
                with self.subTest(defect=defect):
                    directory = base / defect
                    directory.mkdir()
                    marker = directory / 'executed'
                    archive = directory / 'swarm-test-linux-amd64.tar.gz'
                    root = archive.name[:-7]
                    entries = ['install.sh', 'build-info.txt', 'LICENSE', 'THIRD_PARTY_NOTICES.md', 'web/index.html']
                    entries += ['linux-amd64/root/' + n for n in ('swarm', 'swarmdev', 'rebuild', 'swarmsetup', 'swarmtui')]
                    entries += ['linux-amd64/swarmd/' + n for n in ('swarmd', 'swarmctl', 'swarm-fff-search', 'libfff_c.so')]
                    with tarfile.open(archive, 'w:gz') as tar:
                        info = tarfile.TarInfo(root)
                        info.type = tarfile.DIRTYPE
                        info.mode = 0o700 if defect == 'private-directory' else 0o755
                        tar.addfile(info)
                        for entry in entries:
                            data = (f'#!/bin/sh\ntouch "{marker}"\n'.encode() if entry == 'install.sh' else b'fixture\n')
                            info = tarfile.TarInfo(root + '/' + entry)
                            info.mode = 0o755 if entry == 'install.sh' or entry.startswith('linux-amd64/') else 0o644
                            if entry.endswith('/swarmtui'):
                                info.mode = {'private-binary': 0o700, 'writable': 0o777, 'special': 0o4755}.get(defect, info.mode)
                                if defect == 'owner':
                                    info.uid = info.gid = 1234
                            info.size = len(data)
                            tar.addfile(info, io.BytesIO(data))
                    checksum = Path(str(archive) + '.sha256')
                    checksum.write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + '  ' + archive.name + '\n')
                    result = subprocess.run(['bash', str(ROOT/'scripts/verify-release-candidate.sh'), str(archive), str(checksum)], capture_output=True, text=True, timeout=10)
                    if defect == 'none':
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertTrue(marker.exists())
                    else:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn('public access modes are unsafe', result.stderr)
                        self.assertFalse(marker.exists())


if __name__ == '__main__':
    unittest.main()
