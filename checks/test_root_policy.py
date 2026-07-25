#!/usr/bin/env python3
"""Tests for checks/root_policy.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config
from checks.root_policy import ALLOWED, BANNED_PATTERNS, is_banned


def setup_module():
    config.reset_cache()


def test_allowed_list_populated():
    """The allowed_files list should have common root files."""
    assert "README.md" in ALLOWED
    assert "engineering.yaml" in ALLOWED
    assert "go.mod" in ALLOWED


def test_is_banned():
    """Banned pattern matching works correctly."""
    assert is_banned("my_handler.go") is True
    assert is_banned("my_service.go") is True
    assert is_banned("README.md") is False
    assert is_banned("cli.py") is False


def test_banned_patterns_list():
    """Banned patterns should contain the standard Go root patterns."""
    assert any("*_handler.go" in p for p in BANNED_PATTERNS)


def test_run_on_project_root():
    """Running on the actual project root should pass."""
    from checks.root_policy import run
    # We're in the project root, so this should pass
    assert run() == 0
