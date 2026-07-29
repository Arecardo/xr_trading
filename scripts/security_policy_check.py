#!/usr/bin/env python3
"""Repository policy checks for information that must not be committed."""

from __future__ import annotations

import ipaddress
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable
from urllib.parse import urlsplit


MAX_TRACKED_FILE_BYTES = 5 * 1024 * 1024
MAX_SCANNED_FILE_BYTES = 2 * 1024 * 1024

SENSITIVE_BASENAMES = {
    "credentials.json",
    "service-account.json",
    "id_rsa",
    "id_dsa",
    "id_ecdsa",
    "id_ed25519",
}
SENSITIVE_SUFFIXES = {
    ".bak",
    ".db",
    ".dump",
    ".jks",
    ".key",
    ".keystore",
    ".p12",
    ".pem",
    ".pfx",
    ".sqlite",
    ".sqlite3",
}
LOCAL_PORT_HOSTS = {
    "127.0.0.1",
    "::1",
    "localhost",
    "database",
    "market-info",
    "postgres",
}
DOCUMENTATION_HOSTS = {
    "example.com",
    "example.invalid",
}

IPV4_RE = re.compile(r"(?<![\d.])(?:\d{1,3}\.){3}\d{1,3}(?![\d.])")
URL_RE = re.compile(
    r"(?P<url>(?:https?|postgres(?:ql)?)://[^\s\"'<>`\])}]+)",
    re.IGNORECASE,
)
HOST_PORT_RE = re.compile(
    r"(?<![\w.-])(?P<host>[A-Za-z][A-Za-z0-9-]*(?:\.[A-Za-z0-9-]+)+):"
    r"(?P<port>\d{2,5})(?!\d)"
)
PRIVATE_KEY_RE = re.compile(r"-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----")
UNSAFE_COMPOSE_PORT_RE = re.compile(
    r"""^\s*-\s*["']?(?!127\.0\.0\.1:)(?:\$\{[^}]+\}|\d{2,5}):\d{2,5}"""
)


@dataclass(frozen=True)
class Finding:
    path: str
    line: int
    reason: str

    def render(self) -> str:
        location = f"{self.path}:{self.line}" if self.line else self.path
        return f"{location}: {self.reason}"


def tracked_paths(root: Path) -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=root,
        check=True,
        capture_output=True,
    )
    return [
        root / path.decode("utf-8", errors="surrogateescape")
        for path in result.stdout.split(b"\0")
        if path
    ]


def is_allowed_env_template(name: str) -> bool:
    lowered = name.lower()
    return lowered == ".env.example" or lowered.endswith(".env.example")


def check_path(root: Path, path: Path) -> list[Finding]:
    relative = path.relative_to(root).as_posix()
    name = path.name.lower()
    findings: list[Finding] = []

    if name.startswith(".env") and not is_allowed_env_template(name):
        findings.append(Finding(relative, 0, "environment file must not be tracked"))
    if name in SENSITIVE_BASENAMES or path.suffix.lower() in SENSITIVE_SUFFIXES:
        findings.append(Finding(relative, 0, "sensitive key, database, or backup file"))

    try:
        size = path.stat().st_size
    except FileNotFoundError:
        return findings
    if size > MAX_TRACKED_FILE_BYTES:
        findings.append(
            Finding(relative, 0, f"tracked file exceeds {MAX_TRACKED_FILE_BYTES} bytes")
        )
    if size > MAX_SCANNED_FILE_BYTES:
        return findings

    data = path.read_bytes()
    if b"\0" in data:
        return findings
    text = data.decode("utf-8", errors="replace")
    findings.extend(check_text(relative, text))
    return findings


def check_text(relative: str, text: str) -> list[Finding]:
    findings: list[Finding] = []
    for line_number, line in enumerate(text.splitlines(), start=1):
        if PRIVATE_KEY_RE.search(line):
            findings.append(Finding(relative, line_number, "private key material"))

        for match in IPV4_RE.finditer(line):
            try:
                address = ipaddress.ip_address(match.group(0))
            except ValueError:
                continue
            if not address.is_loopback:
                findings.append(
                    Finding(
                        relative,
                        line_number,
                        "non-loopback IP address must come from runtime config",
                    )
                )

        for match in URL_RE.finditer(line):
            raw_url = match.group("url")
            if "$" in raw_url or "{" in raw_url or "}" in raw_url:
                continue
            try:
                parsed = urlsplit(raw_url)
                host = (parsed.hostname or "").lower()
                port = parsed.port
            except ValueError:
                findings.append(Finding(relative, line_number, "malformed network URL"))
                continue
            if (
                parsed.username is not None
                and host not in LOCAL_PORT_HOSTS
                and host not in DOCUMENTATION_HOSTS
            ):
                findings.append(
                    Finding(
                        relative,
                        line_number,
                        "external URL must not contain embedded credentials",
                    )
                )
            if (
                port is not None
                and host not in LOCAL_PORT_HOSTS
                and host not in DOCUMENTATION_HOSTS
            ):
                findings.append(
                    Finding(
                        relative,
                        line_number,
                        "external endpoint with explicit port must come from runtime config",
                    )
                )

        for match in HOST_PORT_RE.finditer(line):
            host = match.group("host").lower()
            if host not in LOCAL_PORT_HOSTS and host not in DOCUMENTATION_HOSTS:
                findings.append(
                    Finding(
                        relative,
                        line_number,
                        "host and port must come from runtime config",
                    )
                )

        if relative.endswith(("compose.yaml", "compose.yml")) and UNSAFE_COMPOSE_PORT_RE.search(
            line
        ):
            findings.append(
                Finding(
                    relative,
                    line_number,
                    "published container port must bind explicitly to 127.0.0.1",
                )
            )
    return findings


def scan(root: Path, paths: Iterable[Path] | None = None) -> list[Finding]:
    selected = list(paths) if paths is not None else tracked_paths(root)
    findings: list[Finding] = []
    for path in selected:
        findings.extend(check_path(root, path))
    return findings


def main() -> int:
    root = Path(__file__).resolve().parents[1]
    findings = scan(root)
    if findings:
        print("repository security policy check failed:", file=sys.stderr)
        for finding in findings:
            print(f"  - {finding.render()}", file=sys.stderr)
        return 1
    print("repository security policy check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
