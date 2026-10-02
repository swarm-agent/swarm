#!/usr/bin/env python3
"""Stamp only an isolated packaging copy; pack SDK/CLI with one immutable image pin."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('image', ROOT / 'scripts/headless-release.py')
image = importlib.util.module_from_spec(spec)
spec.loader.exec_module(image)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', required=True, type=Path)
    parser.add_argument('--version', required=True)
    parser.add_argument('--source', required=True)
    args = parser.parse_args()
    out = args.evidence.resolve()
    metadata = image.verify((out / 'swarm-headless.oci.tar').read_bytes(),
                            image.decode((out / 'headless-image.json').read_bytes()), args.version, args.source)
    image.require(subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip() == args.source,
                  'packaging source mismatch')
    image.require(not subprocess.check_output(['git', 'diff', '--', 'packages'], cwd=ROOT).strip() and
                  not subprocess.check_output(['git', 'diff', '--cached', '--', 'packages'], cwd=ROOT).strip(),
                  'dirty package source')
    rows = []
    with tempfile.TemporaryDirectory(prefix='swarm-pack-', dir=os.environ['TMPDIR']) as tmp:
        for package in ('sdk', 'cli'):
            work = Path(tmp) / package
            # Copy tracked bytes only: no ambient output, dependencies, credentials or ignored files.
            tracked = subprocess.check_output(['git', 'ls-files', '-z', 'packages/' + package], cwd=ROOT).decode().split('\0')
            for name in filter(None, tracked):
                source = ROOT / name
                image.require(source.is_file() and not source.is_symlink(), 'unsafe package source')
                target = work / Path(name).relative_to('packages/' + package)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, target)
                target.chmod(source.stat().st_mode & 0o777)
            path = work / 'package.json'
            manifest = json.loads(path.read_text())
            manifest['version'] = metadata['version']
            path.write_text(json.dumps(manifest, indent=2) + '\n')
            lock_path = work / 'package-lock.json'
            lock = json.loads(lock_path.read_text())
            lock['version'] = metadata['version']
            lock['packages']['']['version'] = metadata['version']
            lock_path.write_text(json.dumps(lock, indent=2) + '\n')
            if package == 'cli':
                (work / 'runtime-image.json').write_text(json.dumps(image.pin(metadata), indent=2) + '\n')
            else:
                subprocess.run(['npm', 'ci', '--ignore-scripts', '--no-audit', '--no-fund'], cwd=work, check=True)
                subprocess.run(['npm', 'run', 'build'], cwd=work, check=True)
            packed = json.loads(subprocess.check_output(['npm', 'pack', '--ignore-scripts', '--json', '--pack-destination', str(out)], cwd=work))
            image.require(len(packed) == 1 and packed[0]['name'] == manifest['name'] and packed[0]['version'] == metadata['version'], 'pack identity mismatch')
            name = packed[0]['filename']
            image.require(Path(name).name == name, 'pack filename')
            rows.append(dict(name=manifest['name'], version=metadata['version'], file=name,
                             sha256=hashlib.sha256((out / name).read_bytes()).hexdigest()))
    (out / 'npm-packages.json').write_text(json.dumps(rows, indent=2) + '\n')


if __name__ == '__main__':
    main()
