#!/usr/bin/env python3
"""Tests for checks/build.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config
from checks.build import run


def setup_module():
    config.reset_cache()


def test_run_returns_int():
    """run() should return 0 (project builds)."""
    ec = run()
    assert ec == 0


def test_binaries_configured():
    """Build config should have at least one binary."""
    cfg = config.get_config()
    assert len(cfg.build.binaries) > 0
    assert cfg.build.binaries[0]["name"] == "aero-vault"
