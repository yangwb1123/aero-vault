#!/usr/bin/env python3
"""Support `python3 -m checks` to run all checks.

Usage:
    python3 -m checks              # Run all checks
    python3 -m checks filesize     # Run specific check
    python3 -m checks help         # List available checks
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

AVAILABLE = [
    "filesize",
    "complexity",
    "architecture",
    "root_policy",
    "coverage",
    "build",
    "invariants",
    "adr_compliance",
    "exemptions",
    "acceptance",
    "health_report",
]


def main():
    args = sys.argv[1:]

    if not args or args[0] in ("help", "--help", "-h"):
        print("Available checks:")
        for name in AVAILABLE:
            print(f"  python3 -m checks {name}")
        print(f"  python3 -m checks all")
        return 0

    if args[0] == "all" or args[0] == "all":
        modules = AVAILABLE
    else:
        modules = [args[0]]

    ec = 0
    for name in modules:
        try:
            mod = __import__(f"checks.{name}", fromlist=["run"])
            ec += mod.run()
        except ImportError:
            print(f"  Unknown check: {name}")
            ec = 1
    return 1 if ec > 0 else 0


if __name__ == "__main__":
    sys.exit(main())
