#!/usr/bin/env python3
"""Requirement-first negative review of fresh-root runtime assertions.

Invariant: installed service processes must retain non-root real/effective/saved/
filesystem credentials; listening TCP sockets must be loopback-only. Authority:
launcher systemd service generation, checked through Linux proc/cgroup state by
scripts/test-install-root-scenario.sh. Execute its actual awk predicates against
synthetic kernel records, including regressions, without needing root or a daemon.
This proves rejection predicates only, not live installation or onboarding.
"""
import pathlib
import re
import subprocess
import unittest

SCENARIO = pathlib.Path(__file__).resolve().parents[2] / "scripts/test-install-root-scenario.sh"


class RootRuntimeAssertions(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = SCENARIO.read_text()
        cls.identities = re.findall(r"awk -v uid=\"\$uid\" -v gid=\"\$gid\" '([^']+)'", source)
        cls.listeners = re.findall(r"awk '([^']+)' \"/proc/\$pid/net/tcp6?\"", source)
        if len(cls.identities) != 2 or len(cls.listeners) != 2:
            raise AssertionError("runtime assertion extraction is incomplete")

    def accepts(self, predicate, records, *variables):
        result = subprocess.run(["awk", *variables, predicate], input=records,
                                text=True, capture_output=True, timeout=5)
        self.assertEqual(result.stderr, "")
        return result.returncode == 0

    def test_all_credential_slots_and_missing_records_fail_closed(self):
        for predicate in self.identities:
            good = "Uid:\t1001\t1001\t1001\t1001\nGid:\t1002\t1002\t1002\t1002\n"
            self.assertTrue(self.accepts(predicate, good, "-v", "uid=1001", "-v", "gid=1002"))
            for field, value in (("Uid", "1001"), ("Gid", "1002")):
                for index in range(4):
                    ids = [value] * 4
                    ids[index] = "0"
                    bad = re.sub(field + r":[^\n]+", field + ":\t" + "\t".join(ids), good)
                    self.assertFalse(self.accepts(predicate, bad, "-v", "uid=1001", "-v", "gid=1002"))
            for bad in ("", good.splitlines()[0] + "\n", good.splitlines()[1] + "\n"):
                self.assertFalse(self.accepts(predicate, bad, "-v", "uid=1001", "-v", "gid=1002"))

    def test_wildcard_and_nonloopback_listeners_rejected(self):
        cases = (("0100007F", "00000000", "0100000A"),
                 ("00000000000000000000000001000000", "0" * 32,
                  "000080FE000000000000000001000000"))
        for predicate, (loopback, wildcard, nonloopback) in zip(self.listeners, cases):
            def row(address, state="0A"):
                return "sl local_address rem_address st\n0: " + address + ":1E61 0:0000 " + state + "\n"
            self.assertTrue(self.accepts(predicate, row(loopback)))
            self.assertFalse(self.accepts(predicate, row(wildcard)))
            self.assertFalse(self.accepts(predicate, row(nonloopback)))
            self.assertTrue(self.accepts(predicate, row(nonloopback, "01")))


if __name__ == "__main__":
    unittest.main()
