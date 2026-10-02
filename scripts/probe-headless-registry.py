#!/usr/bin/env python3
"""Read-only GHCR manifest probe; credentials never enter argv or diagnostics."""
import base64
import hashlib
import json
import os
import re
import sys
import urllib.error
import urllib.request


class Invalid(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise Invalid('registry redirect refused')


def transport(url, headers):
    request = urllib.request.Request(url, headers=headers)
    opener = urllib.request.build_opener(NoRedirect())
    try:
        response = opener.open(request, timeout=30)
    except urllib.error.HTTPError as exc:
        response = exc
    except (OSError, urllib.error.URLError):
        raise Invalid('registry transport failed') from None
    with response:
        body = response.read(4 * 1024 * 1024 + 1)
        if len(body) > 4 * 1024 * 1024:
            raise Invalid('registry response too large')
        return response.code, body


def probe(version, expected, actor, secret, request=transport):
    if not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', version):
        raise Invalid('invalid image tag')
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', expected):
        raise Invalid('invalid expected digest')
    if not actor or not secret or ':' in actor:
        raise Invalid('registry credentials required')
    basic = base64.b64encode((actor + ':' + secret).encode()).decode()
    status, body = request(
        'https://ghcr.io/token?service=ghcr.io&scope=repository:swarm-agent/swarm-headless:pull',
        {'Authorization': 'Basic ' + basic})
    if status != 200:
        raise Invalid('registry authentication failed')
    try:
        token = json.loads(body)['token']
        if not isinstance(token, str) or not token or '\r' in token or '\n' in token:
            raise ValueError()
    except (ValueError, KeyError, TypeError):
        raise Invalid('malformed registry authentication response') from None
    status, body = request(
        'https://ghcr.io/v2/swarm-agent/swarm-headless/manifests/' + version,
        {'Authorization': 'Bearer ' + token,
         'Accept': 'application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json'})
    if status == 200:
        if 'sha256:' + hashlib.sha256(body).hexdigest() != expected:
            raise Invalid('refusing conflicting immutable image tag')
        return 'present'
    if status == 404:
        try:
            errors = json.loads(body)['errors']
            absent = (isinstance(errors, list) and bool(errors) and
                      all(isinstance(e, dict) and e.get('code') in
                          ('NAME_UNKNOWN', 'MANIFEST_UNKNOWN') for e in errors))
        except (ValueError, KeyError, TypeError):
            absent = False
        if absent:
            return 'absent'
    raise Invalid('registry absence not proven; publication refused')


def main():
    try:
        if len(sys.argv) != 3:
            raise Invalid('usage: probe-headless-registry.py VERSION DIGEST')
        print(probe(sys.argv[1], sys.argv[2], os.environ.get('GITHUB_ACTOR', ''),
                    os.environ.get('GH_TOKEN', '')))
    except Exception:
        # Never echo server bodies, URLs, headers, or exception payloads.
        print('GHCR probe failed: authentication, transport, malformed response or conflicting tag; publication refused', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
