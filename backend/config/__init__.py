"""Configuration loading and validation.

Intentionally empty scaffolding for BE-001. Real configuration (DB DSN,
environment gating, market-info-service base URL, etc.) lands with BE-002+;
per security-standards.md §4, secrets must only ever be read from
environment variables / a secret manager, never hardcoded, and startup must
fail closed when required credentials are missing.
"""
