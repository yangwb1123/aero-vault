#!/usr/bin/env python3
"""Tests for checks/architecture.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config
from checks.architecture import classify, all_allowed_deps, check_package, run


def setup_module():
    config.reset_cache()


def test_classify():
    """classify extracts the first meaningful path segment."""
    class FakePath:
        def __init__(self, parts):
            self._parts = parts
        def relative_to(self, other):
            return self
        @property
        def parts(self):
            return self._parts

    p = FakePath(("internal", "service", "file.go"))
    assert classify(p) == "internal/service"

    p = FakePath(("cmd", "server", "main.go"))
    assert classify(p) == "cmd"

    p = FakePath(("sdk", "python", "foo.py"))
    assert classify(p) == "sdk"


def test_run_returns_int():
    """run() should return 0 (architecture WARN-only)."""
    ec = run()
    assert ec == 0
