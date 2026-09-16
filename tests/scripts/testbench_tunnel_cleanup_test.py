#!/usr/bin/env python3
"""Requirement: tunnel heartbeat termination must reap its sleeping child.
Threat: successful provider suites falsely fail or leak processes on tunnel close.
Authority: testbench-container-deploy.sh heartbeat/cleanup; execute the exact shell
function with real children, no SSH/network, under the canonical supervisor.
"""
import importlib.util
import os
from pathlib import Path
import re
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class Cleanup(unittest.TestCase):
    def test_heartbeat_reaps_sleep(self):
        source = (ROOT/'scripts/testbench-container-deploy.sh').read_text()
        heartbeat = re.search(r'    heartbeat\(\) \{.*?\n    \}', source, re.S).group()
        spec = importlib.util.spec_from_file_location('supervisor', ROOT/'scripts/launch-prerun-supervisor.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory(dir=os.environ['TMPDIR']) as tmp:
            command = 'tunnel_pid=$$\nslot_action(){ :; }\n' + heartbeat + '\nheartbeat & pid=$!\nsleep .2\nkill "$pid"\nwait "$pid"\n'
            result = module.run(tmp, 1, [{'id': 'cleanup', 'argv': ['bash', '-c', command]}], wall=3, stall=3)
            self.assertEqual(result, 0)


if __name__ == '__main__':
    unittest.main()
