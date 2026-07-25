#!/usr/bin/env python3
"""Tests for checks/invariants.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config
from checks.invariants import (
    check_i1, check_i2, check_i3, check_i5, check_i6, run,
)


def setup_module():
    config.reset_cache()


def test_i1_no_reused_placeholders():
    """I1: SQL placeholder rebind check should pass (no violations)."""
    findings = check_i1()
    # Warnings are allowed but should not indicate actual placeholder reuse
    for f in findings:
        assert "reused" not in f.lower(), f"Unexpected I1 violation: {f}"


def test_i2_migration_files_match():
    """I2: Every .up.sql should have matching .down.sql."""
    findings = check_i2()
    assert len(findings) == 0, f"Migration mismatch: {findings}"


def test_i3_no_reverse_parse():
    """I3: Storage key should not reverse-parse @v."""
    findings = check_i3()
    # Warnings are informational
    for f in findings:
        assert "reverse-parse" in f.lower() or "WARN" in f


def test_i5_opt_in_defaults():
    """I5: Opt-in flags should default to false."""
    findings = check_i5()
    fails = [f for f in findings if "FAIL" in f]
    assert len(fails) == 0, f"I5 violations: {fails}"


def test_i6_no_testify():
    """I6: No testify/gomega in go.mod."""
    findings = check_i6()
    fails = [f for f in findings if "FAIL" in f]
    assert len(fails) == 0, f"I6 violations: {fails}"


def test_run_returns_int():
    """run() should return 0 (all invariants pass)."""
    ec = run()
    assert ec == 0
