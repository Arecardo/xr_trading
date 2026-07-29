import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "security_policy_check", ROOT / "scripts" / "security_policy_check.py"
)
security_policy_check = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = security_policy_check
SPEC.loader.exec_module(security_policy_check)


class SecurityPolicyCheckTest(unittest.TestCase):
    def check_file(self, relative_path: str, content: str):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / relative_path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
            return security_policy_check.check_path(root, path)

    def test_allows_local_development_endpoints_and_env_template(self):
        findings = self.check_file(
            ".env.example",
            "\n".join(
                [
                    "API_URL=http://127.0.0.1:8090",
                    "DATABASE_URL=postgres://user:local-only@postgres:5432/test",
                ]
            ),
        )
        self.assertEqual(findings, [])

    def test_rejects_non_loopback_ip_and_external_port(self):
        findings = self.check_file(
            "config.txt",
            "INTERNAL=http://" + "10.20." + "30.40:8080\n"
            "PUBLIC=https://" + "service." + "invalid:8443\n",
        )
        reasons = [finding.reason for finding in findings]
        self.assertTrue(any("non-loopback IP" in reason for reason in reasons))
        self.assertTrue(
            any("external endpoint with explicit port" in reason for reason in reasons)
        )

    def test_rejects_sensitive_file_types(self):
        findings = self.check_file("secrets/client.key", "not-a-real-key")
        self.assertTrue(any("sensitive key" in finding.reason for finding in findings))

    def test_rejects_private_key_material(self):
        findings = self.check_file(
            "fixture.txt",
            "-----BEGIN " + "PRIVATE KEY-----\nplaceholder\n-----END PRIVATE KEY-----\n",
        )
        self.assertTrue(any("private key material" in finding.reason for finding in findings))

    def test_rejects_compose_port_without_loopback_binding(self):
        findings = self.check_file(
            "compose.yaml",
            'ports:\n  ' + '- "${SERVICE_PORT:-8090}:8090"\n',
        )
        self.assertTrue(
            any("bind explicitly to 127.0.0.1" in finding.reason for finding in findings)
        )


if __name__ == "__main__":
    unittest.main()
