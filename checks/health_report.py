#!/usr/bin/env python3
"""Health report: file sizes, recent changes, package dependencies."""
import subprocess
import sys
from datetime import datetime
from pathlib import Path

ROOT = Path.cwd()


def run() -> int:
    print("=" * 72)
    print(f"  aero-vault — Engineering Health Report")
    print(f"  {datetime.now().isoformat()}")
    print("=" * 72)

    # ── 1. File size outliers ──
    print("\n--- 1. File Size Report (limit: 500 lines) ---")
    result = subprocess.run(
        [sys.executable, "checks/filesize.py"],
        capture_output=True, text=True, check=False, cwd=str(ROOT),
    )
    print(result.stdout)

    # ── 2. Recent changes (top 10) ──
    print("\n--- 2. Recent Changes (top 10) ---")
    result = subprocess.run(
        "find . -name '*.go' -not -path './.git/*' -not -path './.claude/*'"
        " -printf '%T@ %p\\n' 2>/dev/null",
        shell=True, capture_output=True, text=True, check=False, cwd=str(ROOT),
    )
    lines = sorted(result.stdout.strip().split("\n"), reverse=True)[:10]
    for line in lines:
        if not line.strip():
            continue
        parts = line.split(" ", 1)
        if len(parts) == 2:
            ts = float(parts[0])
            dt = datetime.fromtimestamp(ts).strftime("%m-%d %H:%M")
            print(f"  {dt}  {parts[1]}")

    # ── 3. Package dependency overview ──
    print("\n--- 3. Package Dependency Overview ---")
    pkg_dirs = sorted(d for d in ROOT.iterdir() if d.is_dir() and not d.name.startswith("."))
    for d in pkg_dirs:
        go_files = list(d.rglob("*.go"))
        src = [f for f in go_files if not f.name.endswith("_test.go")]
        if src:
            imports = set()
            for f in src:
                for line in f.read_text().split("\n"):
                    if '"github.com/aero-vault/aero-vault/' in line:
                        imp = line.split('"github.com/aero-vault/aero-vault/')[1].split('"')[0]
                        imports.add(imp)
            if imports:
                print(f"  {d.name}: {len(src)} src files, imports {sorted(imports)}")

    # ── 4. Quick complexity summary ──
    print("\n--- 4. Complexity Summary ---")
    subprocess.run(
        [sys.executable, "checks/complexity.py"],
        cwd=str(ROOT),
    )

    print("\n" + "=" * 72)
    return 0


if __name__ == "__main__":
    sys.exit(run())
