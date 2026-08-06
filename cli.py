#!/usr/bin/env python3
"""aero-vault engineering CLI — cross-platform entry point.

Usage:
    python cli.py <command> [options]

Commands:
    generate              Regenerate engineering scaffolding
    check                 Quick check (filesize + vet)
    check-filesize        File size check
    harness               Full engineering gates (filesize + complexity + architecture)
    complexity            Cyclomatic complexity check
    architecture          Dependency direction check
    coverage              Run test coverage
    build                 Build binaries to bin/
    test                  Run go tests
    race                  Run tests with -race
    vet                   Run go vet
    fmt                   Check gofmt
    lint                  Run golangci-lint
    health-report         Health report
    security-scan         Run govulncheck + gosec
    invariants            Check engineering invariants (I1-I6)
    accept                Full acceptance suite (HARNESS.md)
    adr-compliance        Check ADR compliance
    root-policy           Check root directory for business code violations
    check-exemptions      Check exemption sync
    skill <name> [args..] Run a skill by directory name
    self-test             Run harness self-test (validate checks)
    diagnose              System diagnosis
    setup                 Install required tools (gocyclo, etc.)
    help                  Show this message

Thresholds/paths are declared in engineering.yaml, not hardcoded here —
see checks/config.py.
"""

import argparse
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path.cwd()

# Ensure checks/ is on the path
sys.path.insert(0, str(ROOT))


def run(*args: str, **kwargs) -> subprocess.CompletedProcess:
    return subprocess.run(list(args), capture_output=False, text=True, check=False, **kwargs)


def cmd_generate():
    """Regenerate engineering.yaml from AGENTS.md constraints (future)."""
    print("generate: engineering.yaml already exists; edit it directly.")
    return 0


def cmd_check():
    """Quick check: filesize + vet."""
    ec = cmd_check_filesize()
    ec += run("go", "vet", "./...").returncode
    return 1 if ec > 0 else 0


def cmd_check_filesize():
    from checks.filesize import run as fs_run
    return fs_run()


def cmd_harness():
    """Full engineering gates: filesize + complexity + architecture."""
    ec = cmd_check_filesize()
    ec += cmd_complexity()
    ec += cmd_architecture()
    return 1 if ec > 0 else 0


def cmd_complexity():
    from checks.complexity import run as cx_run
    return cx_run()


def cmd_architecture():
    from checks.architecture import run as ar_run
    return ar_run()


def cmd_coverage():
    from checks.coverage import run as cv_run
    return cv_run()


def cmd_build():
    from checks.build import run as bd_run
    return bd_run()


def cmd_test():
    return run("go", "test", "./...").returncode


def cmd_race():
    return run("go", "test", "-race", "-count=1", "./...").returncode


def cmd_vet():
    return run("go", "vet", "./...").returncode


def cmd_fmt():
    result = subprocess.run(
        "gofmt -l . | grep -v '^.claude/'",
        shell=True, text=True, capture_output=True, check=False,
    )
    if result.stdout and result.stdout.strip():
        print("Unformatted files:", result.stdout, file=sys.stderr)
        return 1
    return 0


def cmd_lint():
    return run(
        "go", "run", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest",
        "run", "--timeout", "5m",
    ).returncode


def cmd_health_report():
    from checks.health_report import run as hr_run
    return hr_run()


def cmd_security_scan():
    r1 = run("go", "run", "golang.org/x/vuln/cmd/govulncheck@latest", "./...")
    if r1.returncode != 0:
        return r1.returncode
    return run(
        "go", "run", "github.com/securego/gosec/v2/cmd/gosec@latest",
        "-quiet", "./...",
    ).returncode


def cmd_invariants():
    from checks.invariants import run as iv_run
    return iv_run()


def cmd_accept():
    from checks.acceptance import run as ac_run
    return ac_run()


def cmd_adr_compliance():
    from checks.adr_compliance import run as adr_run
    return adr_run()


def cmd_setup():
    """Install required development tools and pre-commit hook."""
    import shutil

    print("--- Installing development tools ---")
    gopath = subprocess.run(["go", "env", "GOPATH"], capture_output=True, text=True).stdout.strip()
    gobin = subprocess.run(["go", "env", "GOBIN"], capture_output=True, text=True).stdout.strip()
    if not gobin:
        gobin = gopath + "/bin"
    # Ensure Go bin is in PATH for subsequent steps
    os.environ["PATH"] = os.environ.get("PATH", "") + ":" + gobin

    tools = [
        ("gocyclo", ["go", "install", "github.com/fzipp/gocyclo/cmd/gocyclo@latest"]),
        ("golangci-lint", ["go", "install", "github.com/golangci/golangci-lint/cmd/golangci-lint@latest"]),
    ]
    has_fail = 0
    for name, cmd in tools:
        if shutil.which(name):
            print(f"  {name}: already installed")
            continue
        print(f"  installing {name}...", end=" ")
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode == 0:
            print("OK")
        else:
            print(f"FAILED ({r.stderr.strip()})")
            has_fail = 1

    # Install pre-commit hook
    hook_script = ROOT / "scripts" / "install-hooks.sh"
    if hook_script.exists():
        print("  installing pre-commit hook...", end=" ")
        r = subprocess.run(["bash", str(hook_script)], capture_output=True, text=True)
        if r.returncode == 0:
            print("OK")
        else:
            print(f"FAILED ({r.stderr.strip()})")
            has_fail = 1

    if has_fail:
        print("  Some tools failed to install")
        return 1
    print("  All tools installed, pre-commit hook active")
    return 0


def cmd_self_test():
    """Run harness self-test: validate all check modules load and return int."""
    print("--- self-test: checking all check modules ---")
    errors = 0
    checks = [
        "checks.filesize", "checks.complexity", "checks.architecture",
        "checks.root_policy", "checks.coverage", "checks.build",
        "checks.invariants", "checks.adr_compliance", "checks.exemptions",
    ]
    for mod_name in checks:
        try:
            mod = __import__(mod_name, fromlist=["run"])
            ec = mod.run()
            print(f"  {mod_name}: run() returned {ec} (type={type(ec).__name__})")
            if not isinstance(ec, int):
                print(f"    FAIL: run() did not return int")
                errors += 1
        except Exception as e:
            print(f"  {mod_name}: FAIL — {e}")
            errors += 1
    if errors:
        print(f"  FAIL: {errors} module(s) failed")
    else:
        print("  PASS")
    return 1 if errors else 0


def cmd_diagnose():
    """System diagnosis: check Python, Go, tools, environment."""
    import shutil
    import platform
    _extend_path_with_go_bin()
    print("=" * 60)
    print("  aero-vault — System Diagnosis")
    print("=" * 60)
    diagnostics = _collect_diagnostics(shutil)
    for name, value, ok in diagnostics:
        status = "✅" if ok else "❌"
        print(f"  {status} {name}: {value}")
    ok_count = sum(1 for _, _, ok in diagnostics if ok)
    total = len(diagnostics)
    print(f"\n  {ok_count}/{total} checks passed")
    return 0 if ok_count == total else 1


def _extend_path_with_go_bin() -> None:
    """Extend PATH with Go bin dir so locally installed tools are found."""
    _gopath = subprocess.run(["go", "env", "GOPATH"], capture_output=True, text=True).stdout.strip()
    if _gopath:
        _extra = os.pathsep.join([os.path.join(_gopath, "bin"), os.path.expanduser("~/go/bin")])
        os.environ["PATH"] = _extra + os.pathsep + os.environ.get("PATH", "")


def _collect_diagnostics(shutil):
    """Collect environment diagnostic rows: (name, value, ok)."""
    import platform
    diagnostics = []
    py_ver = sys.version.split()[0]
    diagnostics.append(("Python", py_ver, True))
    go_path = shutil.which("go")
    if go_path:
        r = subprocess.run(["go", "version"], capture_output=True, text=True)
        diagnostics.append(("Go", r.stdout.strip() if r.returncode == 0 else "not found", r.returncode == 0))
    else:
        diagnostics.append(("Go", "not installed", False))
    gocyclo_path = shutil.which("gocyclo")
    diagnostics.append(("gocyclo", gocyclo_path or "not installed", gocyclo_path is not None))
    for tool in ["gofmt", "git", "docker"]:
        found = shutil.which(tool) is not None
        diagnostics.append((tool, "available" if found else "not installed", found))
    from checks.config import get_config
    cfg = get_config()
    diagnostics.append(("engineering.yaml", str(cfg.path), cfg.path.exists()))
    diagnostics.append(("Project", cfg.project.name, bool(cfg.project.name)))
    return diagnostics


def cmd_skill(args: list):
    """Run a skill by directory name."""
    if not args:
        print("Usage: python cli.py skill <name> [args..]", file=sys.stderr)
        skills_dir = ROOT / "skills"
        if skills_dir.is_dir():
            print("\nAvailable skills:", file=sys.stderr)
            for d in sorted(skills_dir.iterdir()):
                if d.is_dir() and not d.name.startswith("_"):
                    print(f"  {d.name}", file=sys.stderr)
        else:
            print("(no skills/ directory)", file=sys.stderr)
        return 1
    name = args[0]
    skill_path = ROOT / "skills" / name / "run.py"
    if not skill_path.exists():
        # Try .md skill (documentation-only skill)
        md_path = ROOT / "skills" / f"{name}.md"
        if md_path.exists():
            print(md_path.read_text(encoding="utf-8"))
            return 0
        print(f"ERROR: skill '{name}' not found at {skill_path}", file=sys.stderr)
        return 1
    return subprocess.run([sys.executable, str(skill_path)] + args[1:]).returncode


def cmd_root_policy():
    from checks.root_policy import run as rp_run
    return rp_run()


def cmd_check_exemptions():
    from checks.exemptions import run as ex_run
    return ex_run()


def cmd_help():
    print(__doc__.strip())
    return 0


COMMANDS = {
    "generate": cmd_generate,
    "check": cmd_check,
    "check-filesize": cmd_check_filesize,
    "harness": cmd_harness,
    "complexity": cmd_complexity,
    "architecture": cmd_architecture,
    "coverage": cmd_coverage,
    "build": cmd_build,
    "test": cmd_test,
    "race": cmd_race,
    "vet": cmd_vet,
    "fmt": cmd_fmt,
    "lint": cmd_lint,
    "health-report": cmd_health_report,
    "security-scan": cmd_security_scan,
    "invariants": cmd_invariants,
    "accept": cmd_accept,
    "adr-compliance": cmd_adr_compliance,
    "setup": cmd_setup,
    "self-test": cmd_self_test,
    "diagnose": cmd_diagnose,
    "skill": cmd_skill,
    "root-policy": cmd_root_policy,
    "check-exemptions": cmd_check_exemptions,
    "help": cmd_help,
}


def main():
    parser = argparse.ArgumentParser(
        prog="cli",
        description="aero-vault engineering CLI",
        add_help=False,
    )
    parser.add_argument("command", nargs="?", default="help", help="Command to run")
    parser.add_argument("args", nargs=argparse.REMAINDER, help="Command arguments")

    parsed, unknown = parser.parse_known_args()
    cmd = parsed.command

    if cmd in ("-h", "--help"):
        cmd = "help"

    if cmd not in COMMANDS:
        print(f"ERROR: unknown command '{cmd}'. Use 'python cli.py help' for usage.", file=sys.stderr)
        return 1

    handler = COMMANDS[cmd]
    if cmd == "skill":
        return handler(parsed.args + unknown)
    return handler()


if __name__ == "__main__":
    sys.exit(main())
