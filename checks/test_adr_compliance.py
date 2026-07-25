#!/usr/bin/env python3
"""Tests for checks/adr_compliance.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config
from checks.adr_compliance import (
    check_adr001, check_adr002, check_adr003, check_adr004, check_adr005, run,
)


def setup_module():
    config.reset_cache()


def test_adr001_mcp_chat():
    """ADR-001: MCP server should have WithChat method."""
    findings = check_adr001()
    assert len(findings) == 0, f"ADR-001 violations: {findings}"


def test_adr002_ai_rate_limiter():
    """ADR-002: Router should accept aiRL parameter."""
    findings = check_adr002()
    assert len(findings) == 0, f"ADR-002 violations: {findings}"


def test_adr003_luhn_check():
    """ADR-003: PII credit_card should use Luhn check."""
    findings = check_adr003()
    assert len(findings) == 0, f"ADR-003 violations: {findings}"


def test_adr004_sse_error():
    """ADR-004: ChatStream error frames."""
    findings = check_adr004()
    assert len(findings) == 0, f"ADR-004 violations: {findings}"


def test_adr005_config_injection():
    """ADR-005: Config vars should exist."""
    findings = check_adr005()
    assert len(findings) == 0, f"ADR-005 violations: {findings}"


def test_run_returns_int():
    """run() should return 0 (all ADRs compliant)."""
    ec = run()
    assert ec == 0
