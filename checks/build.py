#!/usr/bin/env python3
"""Build check: compile all binaries declared in engineering.yaml.

Binary definitions live in engineering.yaml (`build:` section) —
see checks/config.py.
"""
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

ROOT = Path.cwd()
_cfg = get_config().build


def run() -> int:
    print("--- build check ---")
    if not _cfg.binaries:
        print("  (no binaries configured)")
        return 0

    output_dir = ROOT / _cfg.output_dir
    output_dir.mkdir(parents=True, exist_ok=True)
    has_fail = 0

    for b in _cfg.binaries:
        name = b.get("name", "unknown")
        path = b.get("path", "")
        out = output_dir / name
        print(f"  building {name} ({path})...")
        r = subprocess.run(
            ["go", "build", "-trimpath", "-o", str(out), path],
            capture_output=True, text=True, check=False,
        )
        if r.returncode != 0:
            print(f"    FAIL: {r.stderr.strip()}")
            has_fail = 1
        else:
            print(f"    OK → {out}")

    if not has_fail:
        print("  PASS")
    return has_fail


if __name__ == "__main__":
    sys.exit(run())
