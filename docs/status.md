# Build status against the plan (28 November 2026)

The website and application are hosted at hedvi.dev and app.hedvi.dev. The
controlled pilot and unrestricted paid customer onboarding remain separate checks. The pilot uses
Railway in Amsterdam for application compute, PostgreSQL and Valkey, and AWS
Stockholm for SES, S3, queues, keys and monitoring. It is not highly available.

Verified in the controlled pilot: domain DKIM and MAIL FROM, SES Tenant Management,
real delivery to an owned mailbox, simulated delivery/bounce/complaint events,
per-organization suppression, idempotency, independent SNS operations alerts,
signed webhook verification with retry, scheduled database backups and a logical
restore. SES production access was confirmed GRANTED in Stockholm on 27 November 2026. AWS credits do not pay Railway charges.

The open-source review added mode/sender authorization, separate test/live
idempotency namespaces, durable SES ownership claims, atomic MFA/session handling,
resource budgets and stricter deployed configuration. These changes require the
new migrations and an ownership check of any pre-existing SES identities before
a deployed system uses the updated code. Local tests are not a production audit.

Hosted deployment has separate configuration for the verified account-mail sender,
the merchant's Stripe account, its tax settings and canonical legal URLs. Verify
each integration in the deployed environment before enabling its customer flow.
Client-IP resolution uses explicit trusted proxies and signed web attestations.


## Hosted acceptance on 28 November

Production account email was received and the revised logo and layout were
checked in Gmail desktop and by the owner on iPhone. The independently enrolled
owner unlocked the private console and exercised a temporary gift and revocation.
A logical production backup was restored to an isolated database; migration and
retention code completed there, and the clone was removed afterwards.

Real Stripe sandbox Checkout passed for all six Flex/Pro price variants in SEK
and USD, with 25% Swedish VAT. Canadian, Danish and German test cards and foreign
payment details preserved the Swedish invoice address. A 3D Secure challenge,
customer-portal card replacement, cancellation at period end and a full Flex refund
passed. A Stripe test-clock renewal produced a paid subscription-cycle invoice and
exactly one new 100k/2 GiB allowance for the next period; all received billing events
were processed. These checks used isolated staging organizations and no real money.
Six live prices and three business-SaaS products passed restricted-key checks.
Both live keys are privately stored in their respective production services; the
worker passed all thirteen resource-read checks and denied customer/refund writes.
The dedicated live portal and signed 23-event snapshot webhook are configured.
Live billing remains disabled while Stripe support corrects the merchant legal
entity and live tax configuration and payment acceptance are completed.
A sandbox success does not verify live payments.

API and worker revision `a2f6b407c347a04cd4542f406bb477af452a9af4` is deployed to
staging and production, with migrations through 0017. Web revision
`2ccbf1d22cde175484337988d75f86d96094b47c` adds the independent HTTPS status link
and clearer feature-availability copy. All five cloud CI jobs passed,
including PostgreSQL/race tests, mobile WebKit and desktop Chromium checks,
rendered Terraform policy tests, secret scanning and SDK contracts.

The release includes opt-in 30-day remembered login, automatic sending after
domain/provider verification and current terms, and organization-wide sending
limits based on usable plan allowances. MAIL FROM changes preserve domain keys
and pause queued attempts until the selected domain configuration is verified.
Cloudflare bulk import checks for conflicting receiving-mail records first.

Actual restricted AWS credentials passed positive and negative tenant/configuration
checks in both environments. The temporary customer-domain exception was removed;
the same customer identity then passed with the general policy. API and worker
deployment gates now exercise provisioning and customer RAW sending. A deliberately
invalid staging claim failed its gate before promotion. The production public-API
attachment and idempotency test after deployment delivered with one accepted
provider attempt and one consumed reservation. Thirty-four public pages, their
documentation figures and HTTPS headers passed the final HTTP check.

These are bounded acceptance results, not proof of zero future incidents. See the
deployment runbook for ongoing host checks and recovery procedures.

## Implemented features

| Area | Scope |
|---|---|
| API and workers | Durable acceptance, encrypted payloads, tenant RLS, quotas, suppression, retry and uncertain-send handling |
| Accounts | Verified email signup, password recovery, MFA and versioned acceptance receipts for six legal documents |
| Console | Secure sessions, projects, test inbox, per-recipient events, keys, domains, signed webhooks, operator review |
| Billing | Hosted Stripe Checkout and portal, verified invoice-cycle Pro allowances, prepaid Flex, refunds/disputes and reconciliation. Real merchant sandbox and deployment acceptance are separate. |
| Public API | Documented Resend-compatible subset; pinned Node/Python SDK contract tests |
| Documentation | Getting started, DNS, key rotation, attachments, webhooks, limits, troubleshooting and architecture |
| Infrastructure | AWS data plane; optional separate ECS/RDS environments; Railway pilot operated separately |
| Local checks | Go tests with PostgreSQL/race detector, web typecheck/lint/tests/build, Terraform validation and secret/dependency scans |

The remaining roadmap includes team invitations, SMTP, bulk suppression import/export,
batch and scheduled email, inbound email, advanced analytics and AI/MCP integrations.

The source license is AGPL-3.0-only. Original customer integration snippets also
have an MIT permission. No source release is a deliverability, uptime, compliance
or security certification.
