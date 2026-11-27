# Build status against the plan (28 November 2026)

This source is pre-release. Local tests and a controlled hosted pilot are separate
from permission to onboard unrestricted production customers. The pilot uses
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
