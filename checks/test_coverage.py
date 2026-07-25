#!/usr/bin/env python3
"""Tests for checks/coverage.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config
from checks.coverage import run


def setup_module():
    config.reset_cache()


def test_run_returns_int():
    """run() should return 0 (coverage is informational)."""
    ec = run()
    assert ec == 0


def test_coverage_config_exists():
    """Coverage targets should be configured."""
    cfg = config.get_config()
    assert "" in cfg.coverage_targets
    assert cfg.coverage_targets[""] >= 50
