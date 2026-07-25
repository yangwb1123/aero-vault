#!/usr/bin/env python3
"""Engineering invariants gate — checks I1–I6 from AGENTS.md §4.

I1 — SQL placeholder rebind: `s.rebind` rewrites $N → ? by positional order.
      Verify no reused $N numbers in the same statement (scan sql.go).
I2 — Migration double-file: every NNNN_*.up.sql must have a matching .down.sql
      in both sqlite/ and postgres/ dirs.
I3 — Storage key: no reverse-parsing of `@v` suffix in GC or list paths.
I4 — Middleware chain order (documented, not code-checkable).
I5 — Opt-in safety defaults: scan config structs for bool fields defaulting
      to true for AI/pgvector/Qdrant/events/cluster/retention flags.
I6 — Stdlib first: no testify/gomega/assert in go.mod or imports.
"""
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from checks.config import get_config

ROOT = Path.cwd()


# ──────────────────────────────────────────────
# I1 — SQL placeholder rebind
# ──────────────────────────────────────────────

def check_i1() -> list[str]:
    """Scan sql.go for $N placeholder usage: each $N must appear exactly once
    per SQL string (no reused numbers)."""
    sql_file = ROOT / "internal" / "repository" / "sql.go"
    if not sql_file.exists():
        return ["  WARN: sql.go not found, skipping I1"]

    content = sql_file.read_text(encoding="utf-8")
    findings = []

    # Find SQL strings with multiple $N references
    # Look for patterns like `$1, $1` (reused placeholder)
    sql_strings = re.findall(r'"(?:[^"\\]|\\.)*"', content)
    for s in sql_strings:
        nums = [int(n) for n in re.findall(r'\$(\d+)', s)]
        if len(nums) != len(set(nums)):
            findings.append(f"  WARN: I1 — reused placeholder in: {s[:80]}...")

    return findings


# ──────────────────────────────────────────────
# I2 — Migration double-file
# ──────────────────────────────────────────────

def check_i2() -> list[str]:
    """Every NNNN_*.up.sql in migrations/sqlite/ and migrations/postgres/
    must have a matching .down.sql."""
    findings = []
    for subdir in ["sqlite", "postgres"]:
        mig_dir = ROOT / "internal" / "repository" / "migrations" / subdir
        if not mig_dir.is_dir():
            findings.append(f"  WARN: I2 — migrations/{subdir} not found")
            continue
        up_files = set()
        down_files = set()
        for f in mig_dir.iterdir():
            name = f.name
            if name.endswith(".up.sql"):
                up_files.add(name.replace(".up.sql", ""))
            elif name.endswith(".down.sql"):
                down_files.add(name.replace(".down.sql", ""))
        missing_down = up_files - down_files
        missing_up = down_files - up_files
        for m in sorted(missing_down):
            findings.append(f"  WARN: I2 — {subdir}/{m}.up.sql has no matching .down.sql")
        for m in sorted(missing_up):
            findings.append(f"  WARN: I2 — {subdir}/{m}.down.sql has no matching .up.sql")
    return findings


# ──────────────────────────────────────────────
# I3 — Storage key no reverse-parse @v
# ──────────────────────────────────────────────

def check_i3() -> list[str]:
    """No code should parse @v from storage keys to extract version ID.
    The @v suffix is append-only; version_id comes from the DB row."""
    findings = []
    # Search for patterns that suggest @v parsing
    scan_dirs = [ROOT / "internal" / "service", ROOT / "internal" / "storage",
                 ROOT / "internal" / "reconcile", ROOT / "internal" / "repository"]
    for scan_dir in scan_dirs:
        if not scan_dir.is_dir():
            continue
        for f in sorted(scan_dir.rglob("*.go")):
            content = f.read_text(encoding="utf-8")
            # Look for dangerous patterns: splitting/stripping @v from keys
            if re.search(r'@v|strings\.(Split|TrimSuffix).*version', content, re.IGNORECASE):
                # Only flag if it looks like parsing version from storage key
                if re.search(r'(storageKey|storage_key|@v).*(split|trim|pars)', content, re.IGNORECASE):
                    rel = f.relative_to(ROOT)
                    findings.append(f"  WARN: I3 — potential @v reverse-parse in {rel}")
    return findings


# ──────────────────────────────────────────────
# I5 — Opt-in safety defaults
# ──────────────────────────────────────────────

def check_i5() -> list[str]:
    """Scan config struct for opt-in flags that should default to false.
    Check that AI_INDEX_ENABLED, PROMETHEUS_ENABLED, AV_ENABLED, etc.
    are off by default."""
    config_file = ROOT / "internal" / "config" / "config.go"
    if not config_file.exists():
        return ["  WARN: config.go not found, skipping I5"]

    content = config_file.read_text(encoding="utf-8")
    opt_in_vars = [
        "AI_INDEX_ENABLED",
        "AV_ENABLED",
        "REPLICATION_ENABLED",
        "PROMETHEUS_ENABLED",
        "AUTH_PERSIST_KEYS",
        "STORAGE_VERIFY_ON_READ",
        "STORAGE_CB_ENABLED",
        "AI_HYBRID_SEARCH",
        "AI_PII_SCAN",
        "AI_PII_REDACT",
        "AI_PER_TENANT_BUDGETS",
        "RECONCILE_DELETE_ORPHAN_BLOBS",
        "RECONCILE_CLUSTER_SINGLETON",
        "WEBUI_ENABLED",
        "IDEMPOTENCY_HASH_BODY",
    ]
    findings = []
    for var in opt_in_vars:
        # Look for `getEnvBool("VAR", false)` or `getEnv("VAR", "false")`
        pattern = re.escape(var) + r'["\']?\s*,\s*(false|true)'
        m = re.search(pattern, content)
        if m:
            if m.group(1) == "true":
                # WEBUI_ENABLED defaults to true by design
                if var != "WEBUI_ENABLED":
                    findings.append(f"  WARN: I5 — {var} defaults to true (should be false for opt-in)")
        else:
            findings.append(f"  WARN: I5 — {var} not found in config.go")
    return findings


# ──────────────────────────────────────────────
# I6 — Stdlib first (no testify)
# ──────────────────────────────────────────────

def check_i6() -> list[str]:
    """No testify/gomega/assert should appear in go.mod or test imports."""
    findings = []
    # Check go.mod
    gomod = ROOT / "go.mod"
    if gomod.exists():
        content = gomod.read_text(encoding="utf-8")
        for bad in ["testify", "gomega", "goconvey", "assert"]:
            if bad in content:
                findings.append(f"  FAIL: I6 — {bad} found in go.mod (stdlib first policy)")

    # Check test imports
    for f in sorted(ROOT.rglob("*_test.go")):
        content = f.read_text(encoding="utf-8")
        for bad in ['"testify', '"gomega', '"goconvey', '"assert']:
            if bad in content:
                rel = f.relative_to(ROOT)
                findings.append(f"  FAIL: I6 — {bad} imported in {rel}")

    return findings


# ──────────────────────────────────────────────
# Aggregator
# ──────────────────────────────────────────────

def run() -> int:
    print("--- invariants check (I1-I6) ---")
    all_findings = []
    all_findings.extend(check_i1())
    all_findings.extend(check_i2())
    all_findings.extend(check_i3())
    all_findings.extend(check_i5())
    all_findings.extend(check_i6())

    has_fail = 0
    for f in all_findings:
        print(f)
        if f.startswith("  FAIL"):
            has_fail = 1

    if not all_findings:
        print("  PASS (no issues found)")
    elif not has_fail:
        print("  PASS (warnings only)")

    return has_fail


if __name__ == "__main__":
    sys.exit(run())
