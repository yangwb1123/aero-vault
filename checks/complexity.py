#!/usr/bin/env python3
"""Cyclomatic complexity gate.

Thresholds, exempt functions, ignore pattern, and gocyclo path live in
engineering.yaml (`complexity:` section) — see checks/config.py.
"""
import os
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

_cx = get_config().complexity
MAX_CYCLO = _cx.max_cyclomatic
EXEMPT_FUNCS = list(_cx.exempt_functions)
IGNORE_PATTERN = _cx.ignore_pattern


# Extend PATH with Go bin directory so gocyclo is found when installed via
# `go install` (which puts binaries in ~/go/bin/ or $GOPATH/bin).
_go_bin = subprocess.run(
    ["go", "env", "GOPATH"], capture_output=True, text=True
).stdout.strip()
if _go_bin:
    _extra = os.pathsep.join([os.path.join(_go_bin, "bin"), os.path.expanduser("~/go/bin")])
    os.environ["PATH"] = _extra + os.pathsep + os.environ.get("PATH", "")


def is_exempt(name: str) -> bool:
    for e in EXEMPT_FUNCS:
        if e in name:
            return True
    return False


def run() -> int:
    print(f"--- cyclomatic complexity (max {MAX_CYCLO}) ---")

    tool = _cx.gocyclo_path
    if subprocess.run(["which", tool], capture_output=True).returncode != 0:
        print(f"  (tool '{tool}' not found — install via: go install github.com/fzipp/gocyclo/cmd/gocyclo@latest)")
        return 0  # graceful degradation: skip

    result = subprocess.run(
        [tool, "-over", str(MAX_CYCLO), "-ignore", IGNORE_PATTERN, "."],
        capture_output=True, text=True, check=False,
    )

    if result.returncode == 0:
        print("  PASS")
        return 0

    lines = [l for l in result.stdout.strip().split("\n") if l.strip()]
    filtered = [l for l in lines if not is_exempt(l.split()[2] if len(l.split()) >= 3 else "")]
    for l in filtered:
        print(f"  {l}")

    if filtered:
        print(f"  FAIL: {len(filtered)} functions exceed max cyclomatic complexity {MAX_CYCLO}")
        return 1

    print("  (all violations exempted)")
    return 0


if __name__ == "__main__":
    sys.exit(run())
