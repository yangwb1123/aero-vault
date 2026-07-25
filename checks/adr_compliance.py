#!/usr/bin/env python3
"""ADR compliance check — verify architecture decisions are followed.

Rules derived from docs/adr/DECISIONS.md:

ADR-001: MCP write_file/delete_file/chat tools should be conditionally
         exposed via WithChat injection (not always present).
ADR-002: AI rate limiter should be a separate *RateLimiter instance,
         not shared with the global limiter.
ADR-003: PII credit_card rule should include Luhn check.
ADR-004: ChatStream error frames use SSE format.
ADR-005: Configurable params (chunk window, etc.) go through config, not
         constructor params.
"""
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

ROOT = Path.cwd()


def check_adr001() -> list[str]:
    """MCP Server should use WithChat for conditional chat tool exposure."""
    findings = []
    mcp_file = ROOT / "internal" / "mcp" / "server.go"
    if mcp_file.exists():
        content = mcp_file.read_text(encoding="utf-8")
        if "WithChat" not in content:
            findings.append("  WARN: ADR-001 — MCP Server missing WithChat method")
        if "chat" not in content:
            findings.append("  WARN: ADR-001 — MCP Server has no chat field")
    return findings


def check_adr002() -> list[str]:
    """AI rate limiter should be independent from global limiter."""
    findings = []
    router_file = ROOT / "internal" / "api" / "rest" / "router.go"
    if router_file.exists():
        content = router_file.read_text(encoding="utf-8")
        if "aiRL" not in content:
            findings.append("  WARN: ADR-002 — router.go should accept aiRL parameter")
    return findings


def check_adr003() -> list[str]:
    """PII credit_card should use Luhn check."""
    findings = []
    pii_file = ROOT / "internal" / "ai" / "pii.go"
    if pii_file.exists():
        content = pii_file.read_text(encoding="utf-8")
        if "luhn" not in content.lower():
            findings.append("  WARN: ADR-003 — pii.go missing Luhn check for credit_card")
    return findings


def check_adr004() -> list[str]:
    """ChatStream error frames should use SSE event:error format."""
    findings = []
    search_files = list((ROOT / "internal" / "api" / "rest").rglob("*.go"))
    for f in search_files:
        content = f.read_text(encoding="utf-8")
        if "writeSSEError" in content or 'event: error' in content:
            return findings  # found it
    findings.append("  WARN: ADR-004 — no writeSSEError or 'event: error' found in api/rest")
    return findings


def check_adr005() -> list[str]:
    """Configurable params should flow through config, not constructor params."""
    findings = []
    # Search across all config files
    config_dir = ROOT / "internal" / "config"
    if config_dir.is_dir():
        all_content = ""
        for f in config_dir.glob("*.go"):
            all_content += f.read_text(encoding="utf-8")
        expected_vars = ["AI_CHUNK_WINDOW", "AI_CHUNK_OVERLAP", "AI_AGENT_MAX_STEPS"]
        for var in expected_vars:
            if var not in all_content:
                findings.append(f"  WARN: ADR-005 — {var} not found in config")
    return findings


def run() -> int:
    print("--- ADR compliance check ---")
    all_findings = []
    all_findings.extend(check_adr001())
    all_findings.extend(check_adr002())
    all_findings.extend(check_adr003())
    all_findings.extend(check_adr004())
    all_findings.extend(check_adr005())

    for f in all_findings:
        print(f)
    if not all_findings:
        print("  PASS")
    return 1 if all_findings else 0


if __name__ == "__main__":
    sys.exit(run())
