"""Contract tests: resend-python 2.54.0 against Hedvi.

The SDK reads RESEND_API_URL at import time, so it is set before import.
"""
import json
import os
import unittest

API = os.environ.get("HEDVI_API_URL", "http://127.0.0.1:8780")
os.environ["RESEND_API_URL"] = API

import resend  # noqa: E402
from resend.exceptions import ResendError  # noqa: E402

FX = json.loads(os.environ["HEDVI_FIXTURE"])


class ContractTest(unittest.TestCase):
    def use(self, key):
        resend.api_key = key

    def test_send_and_get_in_test_mode(self):
        self.use(FX["test_key"])
        sent = resend.Emails.send({
            "from": "Acme <onboarding@hedvi.dev>", "to": ["ada@example.com"], "subject": "Contract",
            "html": "<p>Hi</p>", "tags": [{"name": "suite", "value": "python"}],
        })
        self.assertTrue(sent["id"])
        got = resend.Emails.get(sent["id"])
        self.assertEqual(got["object"], "email")
        self.assertEqual(got["html"], "<p>Hi</p>")
        self.assertEqual(got["last_event"], "test_captured")

    def test_idempotency(self):
        self.use(FX["test_key"])
        params = {"from": "onboarding@hedvi.dev", "to": "x@example.com", "subject": "Idem", "text": "t"}
        a = resend.Emails.send(params, {"idempotency_key": "py-contract-1"})
        b = resend.Emails.send(params, {"idempotency_key": "py-contract-1"})
        self.assertEqual(a["id"], b["id"])
        with self.assertRaises(ResendError) as ctx:
            resend.Emails.send({**params, "subject": "Other"}, {"idempotency_key": "py-contract-1"})
        self.assertEqual(ctx.exception.code, 409)

    def test_unsupported_field_is_rejected(self):
        self.use(FX["test_key"])
        with self.assertRaises(ResendError) as ctx:
            resend.Emails.send({"from": "a@hedvi.dev", "to": "b@example.com", "subject": "s", "text": "t", "scheduled_at": "in 1 hour"})
        self.assertEqual(ctx.exception.code, 422)
        self.assertIn("scheduled_at", ctx.exception.message)

    def test_live_send_and_domains(self):
        self.use(FX["live_key"])
        sent = resend.Emails.send({"from": f"App <app@{FX['domain']}>", "to": "ok@example.com", "subject": "Live", "text": "t"})
        self.assertTrue(sent["id"])
        domains = resend.Domains.list()
        self.assertEqual(domains["data"][0]["name"], FX["domain"])

    def test_invalid_key(self):
        self.use("re_not_a_hedvi_key")
        with self.assertRaises(ResendError) as ctx:
            resend.Emails.get("00000000-0000-0000-0000-000000000000")
        self.assertEqual(ctx.exception.code, 403)


if __name__ == "__main__":
    unittest.main()
