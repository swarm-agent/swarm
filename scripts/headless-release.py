#!/usr/bin/env python3
"""Validate exact OCI bytes; emit packaging pins, never qualification evidence."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import tarfile

IMAGE = 'ghcr.io/swarm-agent/swarm-headless'


def require(ok, message):
    if not ok:
        raise ValueError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def unique(pairs):
    out = {}
    for key, value in pairs:
        require(key not in out, 'duplicate JSON key')
        out[key] = value
    return out


def decode(raw):
    return json.loads(raw, object_pairs_hook=unique)


def inspect(raw, version, source):
    require(re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version), 'stable version required')
    require(re.fullmatch('[0-9a-f]{40}', source), 'exact source required')
    require(len(raw) <= 1024**3, 'OCI archive size limit')
    files, total = {}, 0
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:') as archive:
        for member in archive:
            name = member.name.rstrip('/')
            require(name in ('blobs', 'blobs/sha256', 'oci-layout', 'index.json') or re.fullmatch(r'blobs/sha256/[0-9a-f]{64}', name), 'unsafe OCI member')
            require(name not in files and len(files) < 256 and not member.pax_headers, 'duplicate/oversized OCI metadata')
            require(member.isdir() or member.isfile(), 'OCI links forbidden')
            total += member.size
            require(0 <= member.size <= 1024**3 and total <= 2 * 1024**3, 'OCI size limit')
            files[name] = archive.extractfile(member).read() if member.isfile() else None
    for name, content in files.items():
        if name.startswith('blobs/sha256/'):
            require(content is not None and sha(content) == name.rsplit('/', 1)[1], 'OCI blob filename mismatch')
    require(decode(files['oci-layout']) == {'imageLayoutVersion': '1.0.0'}, 'OCI layout version')
    index = decode(files['index.json'])
    require(index['schemaVersion'] == 2 and len(index['manifests']) == 1, 'single image required')

    def blob(desc, media):
        require(desc['mediaType'] in media and re.fullmatch(r'sha256:[0-9a-f]{64}', desc['digest']), 'OCI descriptor')
        content = files['blobs/sha256/' + desc['digest'][7:]]
        require(len(content) == desc['size'] and sha(content) == desc['digest'][7:], 'OCI blob mismatch')
        return content

    desc = index['manifests'][0]
    manifest = decode(blob(desc, {'application/vnd.oci.image.manifest.v1+json'}))
    require(manifest['schemaVersion'] == 2, 'OCI manifest version')
    config = decode(blob(manifest['config'], {'application/vnd.oci.image.config.v1+json'}))
    require(config['os'] == 'linux' and config['architecture'] == 'amd64', 'linux/amd64 required')
    labels = config['config']['Labels']
    require(labels['org.opencontainers.image.revision'] == source and labels['org.opencontainers.image.version'] == version, 'image source/version mismatch')
    require(labels['org.opencontainers.image.source'] == 'https://github.com/swarm-agent/swarm', 'image repository mismatch')
    require(config['config'].get('User') == '10001:10001', 'non-root image required')
    require(config['config'].get('Entrypoint') == ['/usr/local/bin/swarmd', '--desktop-port=0', '--cwd=/project'], 'headless entrypoint required')
    require(manifest['layers'], 'image layers required')
    for layer in manifest['layers']:
        blob(layer, {'application/vnd.oci.image.layer.v1.tar+gzip', 'application/vnd.oci.image.layer.v1.tar'})
    return dict(schema='swarm.headless-image/v1', sourceCommit=source, version=version[1:],
                platform='linux/amd64', archiveSha256=sha(raw),
                manifestDigest=desc['digest'], imageId=manifest['config']['digest'])


def verify(raw, metadata, version, source):
    actual = inspect(raw, version, source)
    require(actual == metadata, 'headless metadata mismatch')
    return actual


def pin(metadata):
    return {key: metadata[key] for key in ('version', 'manifestDigest', 'imageId', 'platform', 'sourceCommit')} | {
        'distribution': 'ghcr', 'reference': IMAGE + '@' + metadata['manifestDigest']}


def publication_gate(env):
    require(env.get('GITHUB_REPOSITORY') == 'swarm-agent/swarm' and
            env.get('GITHUB_EVENT_NAME') == 'push' and env.get('GITHUB_REF') == 'refs/heads/main' and
            env.get('RELEASE_ENVIRONMENT') == 'stable-release', 'protected main publication required')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['inspect', 'verify', 'pin', 'publication-gate'])
    parser.add_argument('--archive', type=Path)
    parser.add_argument('--metadata', type=Path)
    parser.add_argument('--version')
    parser.add_argument('--source')
    args = parser.parse_args()
    if args.command == 'publication-gate':
        publication_gate(os.environ)
        return
    require(args.archive and args.version and args.source, 'archive/version/source required')
    require(args.archive.stat().st_size <= 1024**3, 'OCI archive size limit')
    raw = args.archive.read_bytes()
    result = inspect(raw, args.version, args.source)
    if args.command != 'inspect':
        require(args.metadata is not None, 'metadata required')
        result = verify(raw, decode(args.metadata.read_bytes()), args.version, args.source)
    print(json.dumps(pin(result) if args.command == 'pin' else result, sort_keys=True))


if __name__ == '__main__':
    main()
