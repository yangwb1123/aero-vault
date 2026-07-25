#!/usr/bin/env python3
"""Tests for checks/exemptions.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config
from checks.exemptions import run


def setup_module():
    config.reset_cache()


def test_run_returns_zero():
    """If no exemptions exist, run() should return 0."""
    ec = run()
    assert ec == 0


def test_exemptions_list_is_empty():
    """The exemptions list should be empty (all files refactored)."""
    cfg = config.get_config()
    assert len(cfg.filesize.exemptions) == 0, \
        f"Expected 0 exemptions, got {len(cfg.filesize.exemptions)}: {cfg.filesize.exemptions}"
