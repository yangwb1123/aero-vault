#!/usr/bin/env python3
"""Root directory policy gate.

The repo root carries NO production Go — only the files whitelisted in
engineering.yaml (`root_policy:` section). Banned patterns (e.g.
*_handler.go, *_service.go) must never appear at the root.
"""
import fnmatch
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

ROOT = Path.cwd()
_cfg = get_config().root_policy
ALLOWED = set(_cfg.allowed_files)
BANNED_PATTERNS = list(_cfg.banned_patterns)
MAX_FILES = _cfg.max_files


def is_banned(name: str) -> bool:
    for pat in BANNED_PATTERNS:
        if fnmatch.fnmatch(name, pat):
            return True
    return False


def run() -> int:
    print("--- root directory policy check ---")
    violations = []

    root_files = [f for f in ROOT.iterdir() if f.is_file() and not f.name.startswith(".")]
    # Count non-allowed files
    extra = [f for f in root_files if f.name not in ALLOWED]
    if extra:
        for f in extra:
            violations.append(f"  FAIL: {f.name} — not in allowed_files")
            if is_banned(f.name):
                violations.append(f"    (matches banned pattern)")

    if len(root_files) > MAX_FILES:
        violations.append(f"  FAIL: {len(root_files)} root files > max {MAX_FILES}")

    for v in violations:
        print(v)
    if not violations:
        print("  PASS")
    return 1 if violations else 0


if __name__ == "__main__":
    sys.exit(run())
