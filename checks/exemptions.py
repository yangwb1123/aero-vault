#!/usr/bin/env python3
"""Exemption sync check: verify that files listed as exemptions actually exceed
the threshold (so exemptions can't silently become stale).

If a file is exempted but no longer violates the rule, it should be removed
from the exemption list.

NOTE: this check counts raw lines directly rather than calling
filesize.check_file(), because check_file() skips exempted files.
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

ROOT = Path.cwd()
_cfg = get_config().filesize
MAX_LINES = _cfg.max_lines
EXEMPTIONS = list(_cfg.exemptions)
REQUIRED = list(_cfg.required_exemptions)
IGNORE_PATTERNS = list(_cfg.ignore_patterns)


def _is_ignored(rel_path: str) -> bool:
    for pat in IGNORE_PATTERNS:
        if pat in rel_path:
            return True
    return False


def _count_lines(path: Path) -> int:
    return len(path.read_text(encoding="utf-8").splitlines())


def run() -> int:
    print("--- exemption sync check ---")
    has_fail = 0

    # Check all required exemptions exist
    for ex in REQUIRED:
        if ex not in EXEMPTIONS:
            print(f"  FAIL: required exemption '{ex}' is missing from filesize.exemptions")
            has_fail = 1

    # Check that exempted files are actually over the limit
    for ex in EXEMPTIONS:
        p = ROOT / ex
        if not p.exists():
            print(f"  WARN: exempted file no longer exists: {ex}")
            continue
        # Count raw lines (not using filesize.check_file which skips exemptions)
        lines = _count_lines(p)
        if lines <= MAX_LINES:
            print(f"  WARN: '{ex}' ({lines} lines) is exempted but ≤ {MAX_LINES} — remove exemption")

    if not has_fail:
        print("  PASS")
    return has_fail


if __name__ == "__main__":
    sys.exit(run())
