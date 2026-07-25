#!/usr/bin/env python3
"""Test coverage gate.

Thresholds live in engineering.yaml (`coverage.targets`). Uses `go test -cover`
and reports package-level coverage. Missing targets are informational only;
this check never fails (consistent with AGENTS.md which sets 50% as target,
80% as stretch, not a hard gate).
"""
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

_cfg = get_config()
TARGETS = _cfg.coverage_targets
MODULE = _cfg.project.module


def run() -> int:
    print("--- coverage check ---")
    result = subprocess.run(
        ["go", "test", "-cover", "./..."],
        capture_output=True, text=True, check=False,
    )
    if result.returncode != 0:
        print("  (go test failed)")
        print(result.stderr)
        return 0  # don't gate on test failures here

    # Parse "ok  ...  coverage: 45.2% of statements"
    pattern = re.compile(r'^ok\s+\S+\s+[\d.]+s\s+coverage:\s+([\d.]+)%')
    # Also parse "FAIL  ...  coverage: 0.0% of statements" for packages without tests
    fail_pattern = re.compile(r'^FAIL\s+\S+\s+coverage:\s+([\d.]+)%')
    lines = result.stdout.split("\n")
    has_fail_count = 0
    for line in lines:
        m = pattern.search(line)
        if m:
            cov = float(m.group(1))
            print(f"  {cov:5.1f}%  {line.split()[1]}")
            target = TARGETS.get("", 0)
            if target > 0 and cov < target:
                has_fail_count = 1
        m = fail_pattern.search(line)
        if m:
            cov = float(m.group(1))
            print(f"  {cov:5.1f}%  {line.split()[1]} (no tests)")

    if result.returncode != 0 and has_fail_count == 0:
        # Only a concern if packages with tests are below target
        pass

    if has_fail_count:
        print(f"  WARN: some packages below {TARGETS.get('', 0)}% target")
    print("  (coverage is informational — does not fail)")
    return 0  # informational only


if __name__ == "__main__":
    sys.exit(run())
