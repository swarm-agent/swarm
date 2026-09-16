#!/usr/bin/env python3
"""Generate an explicit candidate-only Go overlay; never edit production sources.

Output must be in the caller's disposable directory. Exact source anchors are
required so drift fails closed rather than producing a partially wired build.
"""
import argparse
import json
from pathlib import Path


def render(root, destination):
    root = Path(root).resolve(strict=True)
    destination = Path(destination).resolve(strict=True)
    if root == destination or root in destination.parents:
        raise ValueError('overlay output must be outside the source checkout')
    source = root / 'swarmd/internal/runtime/daemon.go'
    text = source.read_text()
    anchor = '"swarm/packages/swarmd/internal/provider/registry"'
    if text.count(anchor) != 1:
        raise ValueError('runtime import anchor changed')
    text = text.replace(anchor, anchor + '\n "swarm/packages/swarmd/internal/testbenchcodex"')
    anchor = '\tmodelProfileStore := pebblestore.NewModelProfileStore(store)'
    if text.count(anchor) != 1:
        raise ValueError('runtime registry anchor changed')
    text = text.replace(anchor, '\tproviders = testbenchcodex.Registry("/run/testbench-codex.sock")\n\tif err := testbenchcodex.Configure(identitySvc, agentModelSettingsStore, modelSvc); err != nil { return nil, fmt.Errorf("configure testbench: %w", err) }\n' + anchor)
    target = destination / 'testbench-daemon.go'
    target.write_text(text)
    overlay = destination / 'codex-overlay.json'
    overlay.write_text(json.dumps({'Replace': {str(source): str(target)}}))
    return overlay


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    print(render(args.source, args.output))
