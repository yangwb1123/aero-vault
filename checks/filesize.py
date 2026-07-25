#!/usr/bin/env python3
"""File size gate: every non-exempt .go file must be ≤ max_lines.

Thresholds, ignore patterns, and exemptions live in engineering.yaml
(`filesize:` section) — see checks/config.py.
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config, ci_error

ROOT = Path.cwd()
_cfg = get_config().filesize
MAX_LINES = _cfg.max_lines
IGNORE_PATTERNS = list(_cfg.ignore_patterns)
EXEMPTIONS = set(_cfg.exemptions)


def is_ignored(rel_path: str) -> bool:
    for pat in IGNORE_PATTERNS:
        if pat in rel_path:
            return True
    return False


def check_file(path: Path) -> list[str]:
    try:
        rel = str(path.relative_to(ROOT))
    except ValueError:
        # File is outside ROOT (e.g., tmpdir in tests) — use absolute path
        rel = str(path)
    if is_ignored(rel):
        return []
    if rel in EXEMPTIONS:
        return []

    lines = path.read_text(encoding="utf-8").splitlines()
    count = len(lines)
    if count > MAX_LINES:
        ci_error(f"{count} lines > {MAX_LINES}", file=rel, title="File too long")
        return [f"  FAIL: {rel} — {count} lines > {MAX_LINES}"]
    return []


def run(targets: list[Path] = None) -> int:
    print(f"--- filesize check (max {MAX_LINES} lines) ---")
    violations = []
    if targets:
        for p in targets:
            violations.extend(check_file(p.resolve()))
    else:
        for f in sorted(ROOT.rglob("*.go")):
            violations.extend(check_file(f))

    for v in violations:
        print(v)
    if not violations:
        print("  PASS")
    return 1 if violations else 0


if __name__ == "__main__":
    targets = [Path(a) for a in sys.argv[1:]] if len(sys.argv) > 1 else None
    sys.exit(run(targets))
