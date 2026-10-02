#!/usr/bin/env python3
"""Validate and print the exact two npm artifacts, never discover publication by glob."""
import hashlib
import json
from pathlib import Path
import re
import sys
import tarfile


def verify(directory, version):
    if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version):
        raise ValueError('stable package version required')
    rows = json.loads((directory / 'npm-packages.json').read_bytes())
    if not isinstance(rows, list) or len(rows) != 2 or {r['name'] for r in rows} != {'@swarm/sdk', '@swarm/cli'}:
        raise ValueError('exact SDK/CLI packs required')
    files = []
    for row in rows:
        expected = 'swarm-' + row['name'].split('/')[1] + '-' + version[1:] + '.tgz'
        if set(row) != {'name', 'version', 'file', 'sha256'} or row['file'] != expected or row['version'] != version[1:]:
            raise ValueError('package identity mismatch')
        path = directory / expected
        if path.is_symlink() or path.stat().st_size > 64*1024**2 or hashlib.sha256(path.read_bytes()).hexdigest() != row['sha256']:
            raise ValueError('package bytes mismatch')
        with tarfile.open(path, 'r:gz') as archive:
            members = archive.getmembers()
            names = [m.name for m in members]
            if len(names) > 10000 or len(set(names)) != len(names) or sum(m.size for m in members) > 128*1024**2:
                raise ValueError('package size/duplicate limit')
            for member in members:
                if not member.isfile() or not member.name.startswith('package/') or '..' in member.name.split('/'):
                    raise ValueError('unsafe package member')
            member = archive.getmember('package/package.json')
            if member.size > 65536:
                raise ValueError('package manifest limit')
            manifest = json.load(archive.extractfile(member))
            if manifest['name'] != row['name'] or manifest['version'] != row['version']:
                raise ValueError('packed manifest mismatch')
        files.append(str(path))
    if {p.name for p in directory.glob('*.tgz')} != {r['file'] for r in rows}:
        raise ValueError('unexpected package archive')
    return files


if __name__ == '__main__':
    print('\n'.join(verify(Path(sys.argv[1]), sys.argv[2])))
