#!/usr/bin/env python3
"""Tests for checks/config.py."""
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import yaml
from checks import config


def setup_module():
    config.reset_cache()


def test_load_minimal():
    """Minimal config should load with defaults for missing sections."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".yaml", delete=False) as f:
        yaml.dump({"project": {"name": "test"}}, f)
        f.flush()
        cfg = config.load(f.name)
        assert cfg.project.name == "test"
        assert cfg.filesize.max_lines == 500
        assert cfg.complexity.max_cyclomatic == 10


def test_load_full():
    """Full config should parse all sections."""
    data = {
        "project": {"name": "aero-vault", "module": "github.com/aero-vault/aero-vault", "language": "go"},
        "filesize": {"max_lines": 300, "exemptions": ["foo.go"]},
        "complexity": {"max_cyclomatic": 8},
        "architecture": {
            "layers": {"internal/api": ["internal/service"]},
            "excluded_dirs": [".git"],
        },
        "root_policy": {"max_files": 10, "allowed_files": ["README.md"]},
        "coverage": {"targets": {"": 50}},
        "build": {"output_dir": "bin", "binaries": [{"name": "test", "path": "./cmd/test"}]},
    }
    with tempfile.NamedTemporaryFile(mode="w", suffix=".yaml", delete=False) as f:
        yaml.dump(data, f)
        f.flush()
        cfg = config.load(f.name)
        assert cfg.filesize.max_lines == 300
        assert cfg.coverage_targets.get("", 0) == 50
        assert len(cfg.build.binaries) == 1


def test_get_config_caching():
    config.reset_cache()
    c1 = config.get_config()
    c2 = config.get_config()
    assert c1 is c2  # same cached instance


def test_reset_cache():
    config.reset_cache()
    c1 = config.get_config()
    config.reset_cache()
    c2 = config.get_config()
    assert c1 is not c2  # new instance after reset
