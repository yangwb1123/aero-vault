#!/usr/bin/env python3
"""Tests for checks/filesize.py."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from checks import config, filesize


def setup_module():
    config.reset_cache()


def _test_file(path: Path, lines: int) -> Path:
    """Write a file with the given number of lines."""
    path.write_text("\n".join(f"line{i}" for i in range(lines)), encoding="utf-8")
    return path


def test_under_limit(tmp_path):
    f = _test_file(tmp_path / "hello.go", 5)
    violations = filesize.check_file(f)
    # Should pass: 5 lines <= standard max 500
    assert violations == []


def test_over_limit(tmp_path):
    f = _test_file(tmp_path / "hello.go", 600)
    violations = filesize.check_file(f)
    assert len(violations) == 1
    assert "600 lines > 500" in violations[0]


def test_ignored_pattern(tmp_path):
    f = _test_file(tmp_path / "hello_test.go", 600)
    violations = filesize.check_file(f)
    # _test.go is ignored by default pattern
    assert violations == []


def test_empty_file(tmp_path):
    f = _test_file(tmp_path / "empty.go", 0)
    violations = filesize.check_file(f)
    assert violations == []


def test_run_on_project(tmp_path):
    """run() should not crash and return 0 or 1."""
    from checks.filesize import run
    ec = run()
    assert ec in (0, 1)
