# Running your own Hedvi

Hedvi is a Go API and worker with a Next.js console, PostgreSQL and Valkey. The
local simulator supports development without AWS or a mail-sending account.
Self-hosting is an operator responsibility; this repository is pre-release.

## Local development

Follow README.md. Bind PostgreSQL, Valkey and the simulator to loopback. The demo
account and all `*_dev` credentials are public fixtures. Never run `make seed`
against a deployed database. Test keys capture messages and do not send email.

## Deployment

1. Create separate environments and a private PostgreSQL database. Apply
   `scripts/db/init-local.sql` only for local development; for a deployment follow
   [database bootstrap](runbooks/database-bootstrap.md) with new passwords.
2. Provision the AWS data plane in `infra/terraform/modules/hedvi` in your own
   account. The ECS/RDS templates are optional and incur recurring costs.
   Application compute may run elsewhere, provided private DB connectivity and
   HTTPS are configured. Review IAM, account IDs, region and costs before apply.
3. Generate independent random data-encryption and API-pepper keys. Store them
   in your runtime secret manager. Never commit them or paste them into logs.
   Keep an encrypted recoverable copy of encryption keys for your backups.
4. Set `HEDVI_ENV=production`, `HEDVI_AWS_MODE=aws`, `HEDVI_MOCK_DNS=false`,
   `HEDVI_COOKIE_SECURE=true`, public HTTPS URLs, exact allowed origins, separate
   database URLs, private authenticated Valkey and all AWS queue/bucket settings
   from Terraform. See `internal/config/config.go` for the full configuration.
5. Run `/hedvi migrate` once with the migration role, then `/hedvi api` and
   `/hedvi worker` with their own restricted roles. Put HTTPS in front of the API
   and console; expose `/readyz` only as needed for your health checker.
6. Obtain SES sending approval, verify your domains and configure DKIM, MAIL FROM
   and DMARC. SES production access and a verified domain are separate requirements.
   Review each sender before enabling live traffic.
7. Configure independent operational alerts, abuse handling, budgets, encrypted
   backups, retention and a tested restore procedure. Verify retries, suppression
   and uncertain-send handling using your own synthetic test project.
8. Publish the corresponding source for your running version as required by the
   AGPL, including modifications, build files and migrations. Link to it from the
   console and website. Set `NEXT_PUBLIC_HEDVI_SOURCE_URL` at web build time to your
   corresponding-source repository/tag; the default points to the upstream project.
   Keep your secrets and customer data private.

## SES customer tenant isolation

Set `HEDVI_SES_TENANT_PREFIX=hedvi-production-` for production. The staging
pilot keeps `hedvi-`. When omitted, Hedvi derives the prefix from
`HEDVI_ENV`. An explicit mismatch prevents startup. Each project uses its
UUID as 32 lowercase hexadecimal characters after the prefix. A send,
approval or domain association checks the stored name against both the
deployment and the exact project ID. New SES customer identities and tenants
receive an `environment` tag when created. An existing identity must match
both the environment and its `hedvi:domain-claim` UUID before mutation.

For a shared AWS account, deploy separate IAM principals and boundaries
for staging and production. Staging must explicitly deny production tenant
ARNs, including associations and tagging, because the existing staging
prefix is a prefix of production names. Restrict writes and sends to the
owned environment's resources. Use a separately provisioned operator tenant
outside the entire `hedvi-` customer namespace for account mail.
[SES deployment and policy checks](../infra/terraform/AUTH-MAIL.md)

Before upgrading a legacy staging environment:

1. Pause provider writes and sends. Using a trusted operator connection,
   confirm every stored tenant matches its own project. The following check
   should return zero rows.
   ```sql
   SELECT id FROM projects
   WHERE ses_tenant_name <> 'hedvi-' || replace(id::text, '-', '');
   ```
2. Inventory the SES tenants, sending identities, configuration set and
   associations in the intended account and region. For each existing
   identity, compare the exact database domain-claim UUID with its
   `hedvi:domain-claim` tag. Establish the owner of each tenant independently
   from its project ID and associations. Resolve unknown or conflicting
   ownership before applying any tag.
3. With the operator principal, add `environment=staging` to confirmed legacy
   resources that lack it. Preserve existing domain-claim tags and tenant
   names. Install the matching role policies and runtime versions together.
   Verify one staging domain association and synthetic send, plus denial
   for a production tenant with the staging principal.
4. Production uses a separate database and fresh tenant names. Restoring
   staging rows into production requires a controlled review and actual SES
   reprovisioning. Editing tenant names in the database alone does not
   provision or transfer provider resources.

## Account mail, legal documents and billing

Configure a dedicated SES account-mail identity, operator tenant and configuration set. Set the canonical dashboard and legal origins, and publish the exact document versions embedded in the API. Verify a mailbox, complete signup and test password reset with MFA enrolled. Existing pilot sessions and API keys are revoked by migration 0009. [Account security runbook](runbooks/account-security.md)

To support paid plans, apply migration 0010 after account ownership, configure the merchant catalog and supply separate restricted Stripe keys to the API and worker. Register the signed webhook destination and exercise Checkout, renewal, portal cancellation and a refund in a sandbox. The web service only needs the application endpoints. [Hosted billing runbook](runbooks/billing.md)

Your AWS/hosting contracts, privacy notices, payment setup and sending reputation
are your own. StackX's hosted-service agreements and pilot configuration do not
configure or authorize your installation. The open-source code does not include
StackX's credentials, customer data, live Terraform state or payment account.

## Before external users

Treat `docs/status.md` as a scope record, not a release certification. Test
restoration and permissions in your own environment. Do not infer high
availability, an uptime SLA, inbox delivery, legal compliance or readiness for
unrestricted signup from the fact that the source can be built.

## Ingress, credentials and browser configuration

Each deployed command accepts only its own database URL: API, worker/admin, or
migrator. Do not give API a worker or owner credential. API startup checks that its
actual database role cannot bypass tenant RLS. Staff HTTP routes are disabled in
deployed environments; use the audited operator CLI through private access.
Domain operations require direct PostgreSQL connections or **session-mode**
pooling. Transaction/statement-mode PgBouncer is unsupported because provider
mutations use session advisory locks on the same connection.

Use PostgreSQL `sslmode=verify-full` with the provider CA and authenticated
`rediss://` Valkey by default. A Railway deployment can explicitly use
`HEDVI_DATABASE_ALLOW_PRIVATE_CA=true` for `sslmode=require`, and
`HEDVI_VALKEY_ALLOW_PRIVATE_PLAINTEXT=true` for authenticated `redis://`, only on
`*.railway.internal` hosts. Those exceptions rely on Railway's isolated WireGuard
private network and do not apply to public hosts. Review that transport model
for your installation; they do not provide database certificate verification.

Set `HEDVI_TRUSTED_PROXIES` to the exact ingress CIDRs you verified. Forwarded
addresses are ignored from every other peer. For a Next.js console hop, generate
a distinct random 32-byte `HEDVI_PROXY_ATTESTATION_KEY` (base64), supply it only to
API and web, and choose a verified `HEDVI_INGRESS_PROVIDER`:

- `railway`: the public edge must overwrite `X-Real-IP` before Node receives it.
- `alb`: use append-mode X-Forwarded-For and allow web ingress only from that ALB.

The web server removes incoming attestation headers and signs method, rewritten
API path, client IP and timestamp. Never expose its Node port directly with these
modes enabled. Test forged and missing headers and multiple real clients after
any ingress change. Invalid/missing attestations fall back to the API's explicit
trusted-proxy chain, never to a caller-supplied arbitrary client IP.

`HEDVI_API_INTERNAL_URL` is read at web runtime. Set `HEDVI_APP_HOST` at web build time for direct public login/signup links and at runtime for canonical account routes. When hosts are split, the console proxy accepts account API calls only on the app host. Set `HEDVI_SITE_URL` to the public site origin. The app-host root opens the dashboard. The app serves login, signup, account proofs and consent. Public guides and legal documents resolve on the site origin. Optional `HEDVI_SWEDISH_HOST` redirects to `HEDVI_SITE_URL` under `/sv`.
Set `HEDVI_PUBLIC_API_URL`, `NEXT_PUBLIC_HEDVI_SUPPORT_EMAIL` and
`NEXT_PUBLIC_HEDVI_SOURCE_URL` at **web build time**. The contact default is an
invalid example address, so a fork must supply its own support contact. Hosted
StackX legal pages describe StackX's service; replace them with your actual
operator's contracts before serving customers.

Auth/dashboard pages render dynamically with fresh CSP script nonces and are
not cached. Marketing pages retain an inline-script policy for static rendering.
Email previews remove network hints and active embedded markup with an HTML
parser before an independent sandbox and restrictive CSP apply. Remote images
require explicit opt-in; enabling them can reveal the viewer's IP and open time.

## Authentication capacity and domain onboarding

Password hashing has two concurrent slots per process and a bounded wait; overload
returns 503. Login has a durable ten-failure/15-minute lock, and MFA verification
or changes share a five-failure/15-minute budget. Expired locks reset their counts.
Console limits apply per user and organization, with an additional pre-session
IP limit; durable admission quotas remain in PostgreSQL when Valkey is unavailable.

New domains initially expose only a project-specific ownership TXT challenge,
valid for 72 hours. Several projects may challenge the same name. A successful
TXT proof plus operator-approved live sending is required before allocating SES
resources; only then does the project acquire exclusive provider custody and
receive DKIM/MAIL FROM records. Existing identities are never silently transferred
between claims. Follow the operator reconciliation runbook for legacy resources.
