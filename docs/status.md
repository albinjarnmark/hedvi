# Build status against the plan (28 November 2026)

The website and application are hosted at hedvi.dev and app.hedvi.dev. The
email API and live Stripe Checkout are enabled. Live card settlement remains a separate check. The pilot uses
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


## International launch follow-up

The owner has replaced the Sweden-first plan with an international, English-only
product. The language switch is removed, legacy Swedish routes redirect to their
English equivalents, and account onboarding and queued account mail use English.
New accounts default to USD; established billing currencies are preserved. Stripe
Checkout, portal and new customer invoice preferences explicitly request English.
Billing addresses allow Hong Kong and UAE without postal codes; US/CA additionally
require their region. International account tests include canonical invoice countries
and zero-tax paid invoices, without weakening payment or address binding.
Revision `e92053d97887dc214aaee51513a41e8c27111446` is deployed to both environments
with migration0020. The request-format rollout completed in two phases after old
API replicas were removed. Both AWS deployment checks passed, including an actual
customer RAW send. All five CI jobs passed in run46145239275. Production public
checks passed for all34 pages and26 illustrations; legacy Swedish routes redirect
to English, and Safari confirmed the live homepage, dashboard and Checkout.

An app-created sandbox purchase with a German company address charged USD4 plus
USD1 VAT and produced exactly one10,000-recipient Flex grant. The invoice and
customer retained the DE address. Separate sandbox preflights covered DE with/without
a VAT ID, US, GB with a VAT ID, HK without a postal code, and excluded ES/IT
territories. These preflights do not establish tax obligations or country eligibility.
Existing Hedvi-bound Stripe customers now prefer English: four staging customers
and one production customer were updated without changing their addresses/currencies.
A new live English Checkout was verified and expired unpaid; no real charge was made.

Production direct purchases are enabled for all 27 EU billing countries. StackX has completed Stripe Managed Payments onboarding for additional markets. A live unpaid session confirmed account access. Additional markets require a tested managed checkout and entitlement rollout before activation.

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
Stripe support detached the merchant's former shared legal entity. StackX AB,
its supplied VAT number and business ownership are now verified; no active account
tasks remained. Swedish VAT collection is active using the operator-confirmed
Small Seller setting and Tax Basic. Production API and worker now have billing
enabled. The actual app opened a live, correctly branded Flex Checkout at 39 SEK
plus 9.75 SEK VAT. No real card payment was submitted; sandbox settlement and
live unpaid Checkout acceptance are separate evidence. Expiring the unpaid live
order produced a real signed webhook; the worker reconciled it to expired with
zero failures and no allowance grant.

The earlier sending-capacity release was
`0d33c086ed0edb0c64a4789a83f6f9f63e6e5940`; the international follow-up above is now
deployed to staging and production, with migrations through0020. The web release includes
the matching sending-capacity copy; its Source link identifies the shipped build. Usable paid Flex and
Pro allowances have no hourly or daily plan admission cap. Free retains 50/hour
and 200/day; purchased units, abuse protections, manual restrictions and shared
provider capacity remain enforced. Workers pace SES calls before payload reads.
Database lock-order regressions, paid allowances and suppression-work guards are
covered in the PostgreSQL suite. All five cloud CI jobs passed,
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
documentation figures and HTTPS headers passed the previous HTTP check. A new
post-update attachment/idempotency acceptance request passed on 28 November,
with one accepted provider attempt, one delivery event and one consumed unit.

The independent status page obtains current on-demand HTTP observations from a
Cloudflare Worker, retaining operator notices and using the newest valid check.
GitHub push/manual snapshots work, but its scheduled trigger was not observed;
continuous monitoring relies on the independent AWS provider and queue alarms.
SES currently permits 50,000 recipients per rolling day and 14 per second across
the account. Requests for 250,000/day and 100/second are pending, not approved.

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
