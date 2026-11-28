"""Offline checks for the private operator enrollment helper."""
import base64
import json
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from urllib.parse import parse_qs, urlparse
from uuid import uuid4

SCRIPT = Path(__file__).with_name("owner-factor.py")


class OwnerEnrollment(unittest.TestCase):
    def prepare(self, file, uid):
        return subprocess.run(
            [sys.executable, str(SCRIPT), "prepare", "--environment", "test", "--user", uid,
             "--email", "owner@example.test", "--file", str(file)],
            capture_output=True, text=True, check=False,
        )

    def test_private_file_and_no_secret_on_output(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / "enrollment.json"
            uid = str(uuid4())
            result = self.prepare(file, uid)
            self.assertEqual(result.returncode, 0)
            data = json.loads(file.read_text())
            self.assertEqual(stat.S_IMODE(file.stat().st_mode), 0o600)
            self.assertEqual(data["user_id"], uid)
            self.assertEqual(len(base64.b32decode(data["secret"])), 20)
            uri = parse_qs(urlparse(data["otpauth_uri"]).query)
            self.assertEqual(uri["issuer"], ["Hedvi Owner (test)"])
            self.assertEqual(uri["secret"], [data["secret"]])
            self.assertFalse(data["secret"] in result.stdout + result.stderr)
            self.assertNotEqual(self.prepare(file, uid).returncode, 0)
            self.assertTrue(json.loads(file.read_text())["secret"] == data["secret"])

    def test_refuses_repository_and_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "repo"
            repo.mkdir()
            (repo / ".git").mkdir()
            self.assertNotEqual(self.prepare(repo / "seed", str(uuid4())).returncode, 0)
            target = root / "target"
            target.write_text("unchanged")
            link = root / "enrollment"
            link.symlink_to(target)
            self.assertNotEqual(self.prepare(link, str(uuid4())).returncode, 0)
            self.assertEqual(target.read_text(), "unchanged")


if __name__ == "__main__":
    unittest.main()
