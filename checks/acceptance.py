#!/usr/bin/env python3
"""Full acceptance suite — validates the complete HARNESS.md pipeline.

Runs: fmt → vet → build → test → filesize → invariants → root-policy
in sequence. Any failure stops the pipeline (fail-fast).
"""
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
ROOT = Path.cwd()


def step(name: str, cmd: list[str]) -> int:
    print(f"\n=== {name} ===")
    r = subprocess.run(cmd, cwd=str(ROOT))
    if r.returncode != 0:
        print(f"  FAILED at step: {name}")
    return r.returncode


def run() -> int:
    print("=" * 60)
    print("  aero-vault Acceptance Suite (HARNESS.md)")
    print("=" * 60)

    steps = [
        ("gofmt", [sys.executable, "cli.py", "fmt"]),
        ("root-policy", [sys.executable, "checks/root_policy.py"]),
        ("filesize", [sys.executable, "checks/filesize.py"]),
        ("invariants", [sys.executable, "checks/invariants.py"]),
        ("vet", ["go", "vet", "./..."]),
        ("build", [sys.executable, "checks/build.py"]),
        ("test", ["go", "test", "./..."]),
    ]

    for name, cmd in steps:
        ec = step(name, cmd)
        if ec != 0:
            print(f"\n*** ACCEPTANCE FAILED at step: {name} ***")
            return 1

    print("\n" + "=" * 60)
    print("  ACCEPTANCE PASSED")
    print("=" * 60)
    return 0


if __name__ == "__main__":
    sys.exit(run())
