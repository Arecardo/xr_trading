"""Regression test: domain/ must not depend on HTTP/DB/Provider infrastructure.

python-backend-standards.md §1 and CLAUDE.md 黄金规则 require that the
domain layer never imports fastapi, httpx, a DB driver, an ORM, or a
Provider/Broker SDK. This test enforces the rule mechanically (source-level
scan) so a future edit that adds such an import fails CI immediately,
rather than relying on someone noticing during review.
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

DOMAIN_ROOT = Path(__file__).resolve().parents[2] / "domain"

FORBIDDEN_TOP_LEVEL_MODULES = {
    "fastapi",
    "starlette",
    "httpx",
    "psycopg",
    "psycopg2",
    "sqlalchemy",
    "asyncpg",
    "requests",
    "aiohttp",
    "uvicorn",
}


def _domain_python_files() -> list[Path]:
    return sorted(DOMAIN_ROOT.rglob("*.py"))


def _imported_top_level_modules(source: str) -> set[str]:
    tree = ast.parse(source)
    modules: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                modules.add(alias.name.split(".")[0])
        elif isinstance(node, ast.ImportFrom) and node.module is not None and node.level == 0:
            modules.add(node.module.split(".")[0])
    return modules


@pytest.mark.parametrize(
    "path", _domain_python_files(), ids=lambda p: str(p.relative_to(DOMAIN_ROOT))
)
def test_domain_module_has_no_infra_imports(path: Path) -> None:
    source = path.read_text(encoding="utf-8")
    imported = _imported_top_level_modules(source)
    leaked = imported & FORBIDDEN_TOP_LEVEL_MODULES
    assert not leaked, f"{path} imports forbidden infrastructure module(s): {leaked}"


def test_domain_root_actually_has_python_files_to_check() -> None:
    # Guards against the parametrized test above silently collecting zero
    # cases if DOMAIN_ROOT is ever misconfigured.
    assert len(_domain_python_files()) >= 7 * 2  # 7 subpackages x (models.py + __init__.py)
