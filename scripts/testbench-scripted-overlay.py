#!/usr/bin/env python3
"""Generate a test-only Go overlay wiring the scripted model; never edit production sources.

The scripted model (swarmd/internal/testbenchscripted) forwards every model
step to the URL in SWARM_SCRIPTED_MODEL_URL so a harness can play a hijacked
model against a real daemon. Only test images built with this overlay have it.

Output must be in the caller's disposable directory. Exact source anchors are
required so drift fails closed rather than producing a partially wired build.
"""
import argparse
import json
from pathlib import Path


def render(root, destination, disable_enforcement=False):
    root = Path(root).resolve(strict=True)
    destination = Path(destination).resolve(strict=True)
    if root == destination or root in destination.parents:
        raise ValueError('overlay output must be outside the source checkout')
    source = root / 'swarmd/internal/runtime/daemon.go'
    text = source.read_text()
    anchor = '"swarm/packages/swarmd/internal/provider/registry"'
    if text.count(anchor) != 1:
        raise ValueError('runtime import anchor changed')
    text = text.replace(anchor, anchor + '\n "swarm/packages/swarmd/internal/testbenchcodex"\n "swarm/packages/swarmd/internal/testbenchscripted"')
    anchor = '\tmodelProfileStore := pebblestore.NewModelProfileStore(store)'
    if text.count(anchor) != 1:
        raise ValueError('runtime registry anchor changed')
    text = text.replace(anchor, '\tproviders = testbenchscripted.Registry(os.Getenv("SWARM_SCRIPTED_MODEL_URL"))\n\tif err := testbenchcodex.Configure(identitySvc, agentModelSettingsStore, modelSvc); err != nil { return nil, fmt.Errorf("configure testbench: %w", err) }\n' + anchor)
    target = destination / 'scripted-daemon.go'
    target.write_text(text)
    replace = {str(source): str(target)}
    if disable_enforcement:
        # Deliberately broken build for layer-independence tests: tool
        # enforcement and permission checks are off, so only the image itself
        # stands between the model and command execution. Never ship it.
        for rel, anchor, insert in (
            ('swarmd/internal/run/provider_tool_invoker.go',
             'func providerManagedToolOffered(config providerToolInvokerConfig, name string) bool {',
             '\n\treturn true // TEST BUILD: enforcement disabled'),
            ('swarmd/internal/permission/service.go',
             'func (s *Service) AuthorizeToolCall(input AuthorizationInput) (AuthorizationResult, error) {',
             '\n\treturn AuthorizationResult{Decision: AuthorizationApprove, Source: "test-build"}, nil // TEST BUILD: permissions disabled'),
        ):
            file = root / rel
            text = file.read_text()
            if text.count(anchor) != 1:
                raise ValueError(f'{rel} anchor changed')
            broken = destination / ('broken-' + file.name)
            broken.write_text(text.replace(anchor, anchor + insert))
            replace[str(file)] = str(broken)
    overlay = destination / 'scripted-overlay.json'
    overlay.write_text(json.dumps({'Replace': replace}))
    return overlay


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--disable-enforcement', action='store_true',
                        help='also turn off tool enforcement and permission checks (layer-independence tests only)')
    args = parser.parse_args()
    print(render(args.source, args.output, args.disable_enforcement))
