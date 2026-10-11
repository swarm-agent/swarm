"""Packaging contract checks; not daemon/runtime qualification.

Requires Python 3 and Docker BuildKit/buildx or Podman (CONTAINER_ENGINE).
Context tests execute the engine's real ignore implementation in a bounded
scratch build (no network/base image).
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
ENGINE = os.environ.get('CONTAINER_ENGINE', 'docker')
if ENGINE not in ('docker', 'podman'):
    raise ValueError('CONTAINER_ENGINE must be docker or podman')


class HeadlessPackagingTests(unittest.TestCase):
    def test_context_allowlist(self):
        """Requirement: send only build inputs, never common private/generated data.

        Threat: a negation leaks a secret or drops embedded assets/FFF. Authority:
        root .dockerignore at Docker's context boundary. A real scratch COPY with
        sentinel files is the narrowest layer proving Docker filtering semantics;
        it does not establish that arbitrary source files contain no secrets.
        """
        allowed = [
            'go.mod', 'go.sum', 'pkg/buildinfo/buildinfo.go',
            'theme/theme.go', 'internal/safefile/file.go',
            'swarmd/go.mod', 'swarmd/go.sum', 'swarmd/cmd/swarmd/main.go',
            'swarmd/internal/fff/include/fff.h',
            'swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so',
            'swarmd/internal/model/snapshotdata/snapshot.json',
            'swarmd/internal/model/snapshotdata/snapshot-version.json',
            'swarmd/internal/runtime/artifact_v3_preview_selection.js',
            'LICENSE', 'THIRD_PARTY_NOTICES.md',
            'containers/headless/inspect.sh', 'containers/headless/FFF-LICENSE',
        ]
        denied = [
            '.git/config', '.env', '.env.production', 'private.pem',
            'swarmd/.git/config', 'swarmd/.env', 'swarmd/internal/private.key',
            'swarmd/internal/credentials.go', 'swarmd/internal/secrets/key.go',
            'swarmd/node_modules/a/index.go', 'swarmd/dist/generated.go',
            'swarmd/build/generated.go', 'swarmd/.cache/cached.go',
            'swarmd/.swarm/session.go', 'swarmd/internal/foo_test.go',
            'swarmd/internal/testdata/private.go', 'swarmd/vendor/private.go',
            'web/dist/index.html', 'backup.json', 'unrelated/main.go',
        ]
        with tempfile.TemporaryDirectory(dir=os.environ['TMPDIR']) as scratch:
            scratch = Path(scratch)
            context, output = scratch / 'context', scratch / 'output'
            context.mkdir()
            (context / '.dockerignore').write_bytes((ROOT / '.dockerignore').read_bytes())
            for name in allowed + denied:
                target = context / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text('context sentinel\n')
            # Log to scratch, not unbounded captured output.
            with (scratch / 'build.log').open('w+') as log:
                command = (['docker', 'buildx', 'build'] if ENGINE == 'docker'
                           else ['podman', 'build', '--jobs=1'])
                result = subprocess.run(
                    command + ['--network=none',
                               '--output', f'type=local,dest={output}', '-f', '-', str(context)],
                    input='FROM scratch\nCOPY . /\n', text=True,
                    stdout=log, stderr=log, timeout=120, check=False,
                )
                log.seek(0, 2)
                log.seek(max(0, log.tell() - 8000))
                self.assertEqual(result.returncode, 0, log.read())
            actual = {p.relative_to(output).as_posix() for p in output.rglob('*') if p.is_file()}
            self.assertEqual(actual, set(allowed))
            for name in denied:
                self.assertFalse((output / name).exists(), name)

    def test_declared_runtime_contract(self):
        """Requirement: packaging retains non-root/headless/local-only defaults,
        starts with permission policy locked, and keeps agent worktrees on a
        persistent volume.

        Threat: recipe edits accidentally expose ports, lose persistent roots
        (including uncommitted agent work), or let agents/remote clients change
        permission policy. Authority: Dockerfile USER/ENTRYPOINT/VOLUME/ENV
        declarations, config.Parse --desktop-port/--lock-permission-policy flags
        and appstorage.WorktreeDataDir (XDG_DATA_HOME). This recipe-level check catches declaration drift;
        only a built-image inspection can establish installed binaries/libraries.
        """
        text = (ROOT / 'Dockerfile').read_text()
        instructions = [line.strip() for line in text.splitlines()]
        self.assertIn('USER 10001:10001', instructions)
        self.assertFalse(any(line.startswith('EXPOSE ') for line in instructions))
        entrypoint = next(line.removeprefix('ENTRYPOINT ') for line in instructions if line.startswith('ENTRYPOINT '))
        self.assertEqual(json.loads(entrypoint), ['/usr/local/bin/swarmd', '--desktop-port=0', '--cwd=/project', '--lock-permission-policy'])
        volumes = next(line.removeprefix('VOLUME ') for line in instructions if line.startswith('VOLUME '))
        self.assertEqual(set(json.loads(volumes)), {'/etc/swarmd', '/var/lib/swarmd', '/var/cache/swarmd', '/var/log/swarmd'})
        self.assertIn('ENV HOME=/home/swarm SWARM_DISABLE_MINT_REPORT=1 XDG_DATA_HOME=/var/lib/swarmd/user-data', instructions)
        self.assertNotIn('bypass-permissions', text)


if __name__ == '__main__':
    unittest.main()
