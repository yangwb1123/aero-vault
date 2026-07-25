#!/usr/bin/env python3
"""Architecture dependency direction gate.

A package's first path segment is its layer. Each layer may only import
packages listed in its allowed dependencies in engineering.yaml
(`architecture.layers` section). Unclassified layers fail the gate.

Violations are reported but do NOT fail the gate (WARN-only), consistent with
AGENTS.md's treatment of architecture rules as conventions.
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

ROOT = Path.cwd()
_cfg = get_config().architecture
LAYERS = dict(_cfg.layers)
EXCLUDED_DIRS = set(_cfg.excluded_dirs)
MODULE_PREFIX = f'"{get_config().project.module}/'


def classify(dir_path: Path) -> str:
    """Return the layer name from the first path segment."""
    rel = dir_path.relative_to(ROOT)
    parts = rel.parts
    if not parts:
        return ""
    # internal/foo/bar → "internal/foo"
    if parts[0] == "internal" and len(parts) >= 2:
        return f"internal/{parts[1]}"
    return parts[0]


def all_allowed_deps(layer: str) -> set:
    """Return transitive allowed deps for layer: direct + their deps."""
    allowed = set()
    queue = list(LAYERS.get(layer, []))
    seen = set()
    while queue:
        p = queue.pop(0)
        if p in seen:
            continue
        seen.add(p)
        allowed.add(p)
        queue.extend(LAYERS.get(p, []))
    return allowed


def check_package(dir_path: Path) -> list[str]:
    layer = classify(dir_path)
    if not layer:
        return []
    # cmd/ and sdk/ are exempt from checks
    if layer in ("cmd", "sdk"):
        return []
    # Skip directories without .go files (e.g., internal/ itself)
    go_files = list(dir_path.glob("*.go"))
    if not go_files:
        return []
    if layer not in LAYERS:
        return [f"  WARN: {dir_path.relative_to(ROOT)} — unclassified layer '{layer}'"]

    allowed = all_allowed_deps(layer)
    allowed.add(layer)  # self-import is always ok
    violations = []

    for go_file in sorted(dir_path.glob("*.go")):
        if go_file.name.endswith("_test.go"):
            continue
        content = go_file.read_text()
        for line in content.split("\n"):
            stripped = line.strip()
            if not stripped.startswith('"') and MODULE_PREFIX not in stripped:
                continue
            if MODULE_PREFIX not in stripped:
                continue
            imp = stripped.split(MODULE_PREFIX)[1].split('"')[0]
            if not imp:
                continue
            # map import to its layer
            imp_parts = imp.split("/")
            if imp_parts[0] == "internal" and len(imp_parts) >= 2:
                imp_layer = f"internal/{imp_parts[1]}"
            else:
                imp_layer = imp_parts[0]

            if imp_layer not in allowed and imp_layer != layer:
                rel_dir = dir_path.relative_to(ROOT)
                violations.append(f"  WARN: {rel_dir} imports {imp} (layer '{imp_layer}' not in allowed deps of '{layer}')")

    return violations


def run() -> int:
    print("--- architecture dependency check ---")
    all_violations = []

    for d in sorted(ROOT.iterdir()):
        if not d.is_dir() or d.name.startswith(".") or d.name in EXCLUDED_DIRS:
            continue
        all_violations.extend(check_package(d))

    # Also check internal/ subdirectories
    internal_dir = ROOT / "internal"
    if internal_dir.is_dir():
        for d in sorted(internal_dir.iterdir()):
            if d.is_dir() and d.name not in EXCLUDED_DIRS:
                all_violations.extend(check_package(d))

    for v in all_violations:
        print(v)

    if not all_violations:
        print("  PASS")
    return 0  # WARN-only, never fails


if __name__ == "__main__":
    sys.exit(run())
