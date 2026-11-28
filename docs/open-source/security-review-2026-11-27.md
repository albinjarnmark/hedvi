# Source-release security review — 27 November 2026

This is a review record for the initial pre-release source snapshot, not a
penetration-test certificate or permission to onboard unrestricted customers.
The main implementation/review agent was Codex. Two additional independent code
reviews used GPT-6.2 Astra (xhigh) and GPT-6 Astra (xhigh). Automated review can miss
vulnerabilities; reports and reproducible regressions are welcome through SECURITY.md.

[External audit triage](external-audit-triage-2026-11-27.md) records the subsequent review of the two external reports, including code changes, regression coverage, operator configuration and remaining hosted release requirements.

## Scope and changes

- Separate test/live and sender-domain authorization for message reads, shared
  domain operations and usage. Separate idempotency namespaces; preserve legacy
  idempotency tombstones and reject unauthorized replay.
- Make TOTP consumption, session creation and MFA changes atomic. Revoke existing
  sessions on privileged bootstrap; require MFA and independently checked user UUID
  and account ownership before a staff grant.
- Issue per-project DNS challenges. Require current TXT proof before allocating a global SES identity. The 28 November self-service update additionally requires current accepted service documents and an unrestricted organization; it replaces the original operator-approval gate. Per-organization resource limits remain, while aggregate resource growth across separate free organizations requires monitoring. Existing reservations retain their immutable claim tags through ambiguous operations and deletion retries.
- Bound authentication work and failed MFA attempts, validate proxy-derived addresses, isolate public API credentials from privileged worker access, and check effective database permissions at startup.
- Validate outbound headers and addresses, perform pre-upload quota checks, confirm sending authorization at commit, and atomically audit idempotent console resends.
- Fence worker claims, bind SES events to their email, preserve unresolved settlement records, and handle complaint suppression precedence. Sanitize email previews and use a nonce-based script policy for authenticated console and sign-in pages.
- Preserve uploaded content when COMMIT may have succeeded but its response was
  lost; reconcile using idempotency and delayed orphan cleanup. Regression tests
  simulate both committed and rolled-back transactions returning transport errors.
- Bound retained live payload storage and count suppressed recipients in operational
  admission windows without charging commercial sending credits.
- Recheck domain status before sending queued messages. Use unique worker lease
  owners for each process. Reject unsafe deployed configuration and known demo keys.
- Serialize resource limits across concurrent database transactions. Bound project,
  API-key and webhook creation. Share API rate limits across organization keys.
- Keep the migration owner credential out of continuously running ECS API/worker
  tasks. The optional ECS workflow builds images only; schema migration is a
  separately reviewed maintenance operation and is not claimed as live-tested.
- Pin CI actions by commit SHA, scan dependencies and secrets, retain third-party
  license notices, and export an allowlisted source tree without private Git history.
- Correct retention documentation: immutable webhook event copies include the
  subject and follow organization metadata retention, separately from email content.

## Publication checks

Use a fresh export and check the exact tree being published:

```sh
python3 scripts/test_export_source.py
python3 scripts/export-source.py /tmp/hedvi-source-new
# Run the pinned gitleaks scanner against that directory and its new Git history.
# Run the CI checks from .github/workflows/ci.yml against the exported tree.
```

Do not publish the development checkout's Git objects, backups, deployment state,
local credentials, private account configuration or build caches. Source scanning
is a check, not a guarantee that every secret format will be recognized.

## Limits and release requirements

The review covers source and synthetic tests, not an external adversarial
penetration test, every effective deployed IAM policy, or a complete binary/image
software bill of materials. The retained dependency inventory describes the
resolved source dependencies and its explicit platform scope.

The 28 November source includes mailbox verification for signup and recovery, and records agreements and notice acknowledgements against six versioned legal documents (migration 0009). Stripe Checkout, the customer portal, verified invoice-based allowances, refunds and disputes are implemented with migration 0010. [Account ownership tests](../../apps/api/internal/e2e/account_ownership_test.go) · [Legal manifest](../../apps/api/internal/legal/manifest.json) · [Billing tests](../../apps/api/internal/e2e/billing_test.go)

Before enabling hosted accounts, exercise signup and recovery through the deployment’s account-mail sender and verify receipts against its published legal URLs. Before enabling payments, test purchases, renewals and reversals in the merchant’s Stripe sandbox. Each deployment also needs checks of proxy trust, ingress header handling and effective IAM. The controlled pilot runs without high availability. [Build and pilot status](../status.md) · [Billing acceptance checklist](../runbooks/billing.md#sandbox-verification)

Valkey rate limits fall back to per-process limits during an outage; durable
acceptance budgets remain in PostgreSQL. Self-host operators must validate their
own IAM, secrets, network access, backups, abuse handling and deployment sequence.
The number of reviewers and passing tests is not a guarantee of security.
