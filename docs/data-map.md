# Data map and retention

Version: 27 November 2026. This describes the controlled hosted pilot. A self-hosted
installation must publish its own map. The primary application/database region is
Railway EU Amsterdam; the email/data plane is AWS Stockholm. Provider account,
support, security and billing processing may occur elsewhere under their contracts.
This is not a claim that every processing operation stays in the EU.

| Data | Primary location | Protection | Retention |
|---|---|---|---|
| Email content: subject in the email record, HTML, text, headers and attachments | AWS S3 Stockholm; subject also in PostgreSQL Amsterdam | Application AES-256-GCM for S3 payloads with tenant/object binding; S3 SSE-KMS; database access controls for the subject | Configured 0–7 days; retained while queued/retrying, queue deadline 72 h; S3 lifecycle backstop 11 days. Webhook copies of subjects have the separate retention below |
| Recipients, statuses, provider events and send attempts | PostgreSQL Amsterdam | Private network, database TLS and hosting storage protection | Free 14 d, Flex 30 d, Pro 90 d; shorter organization settings where supported |
| Test inbox | S3 Stockholm and PostgreSQL Amsterdam | Same protections; no provider send | 7 days |
| Idempotency responses | PostgreSQL Amsterdam | Contains the accepted email ID, not content | 24 h, followed by periodic cleanup |
| Webhook events and attempts, including subject, sender and recipients in the event payload | PostgreSQL Amsterdam | Database access controls; encrypted signing secrets; HTTPS delivery with HMAC signatures | Organization metadata retention (Free 14 d, Flex 30 d, Pro 90 d); first 1 KB of receiver response retained for debugging. Shorter email-content retention does not erase these event copies |
| Published outbox jobs | PostgreSQL Amsterdam | Contains identifiers, not message content | 7 days after publication |
| Queue messages | AWS SQS Stockholm | SSE-KMS | Main queues up to 4 days; DLQs 14 days. SES events include addresses and SMTP diagnostics |
| Email transport | AWS SES Stockholm, then recipient mail systems | AWS service protections; opportunistic destination TLS | AWS and receiving-provider processing policies; destination systems may be worldwide |
| Account data | PostgreSQL Amsterdam | Password hashing; no plaintext passwords | Account lifetime, followed by closure processing; statutory or security retention where applicable |
| Account email verification and recovery | PostgreSQL Amsterdam, dedicated SES sender in Stockholm, recipient mailbox | Purpose-bound token digests and sealed email queue payloads | Links expire after 30 minutes. Account emails are outside the organization’s test inbox and send quota. See the [account security runbook](runbooks/account-security.md). |
| Legal acceptance receipts | PostgreSQL Amsterdam | User and document version, hash, origin and acknowledgement kind | Contract and evidence retention are separate from message-content settings. |
| Sessions and security audit | PostgreSQL Amsterdam | HttpOnly cookies; hashed tokens; TOTP secrets encrypted | Sessions 14 d maximum / 3 d idle; expired records cleaned periodically; audit 400 d |
| Rate-limit entries | Valkey Amsterdam | Private authenticated connection | Seconds to minutes; no email bodies |
| Automatic database backups | Railway EU volume environment | Provider storage protection | Daily snapshots retained 6 days, weekly 27 days. PITR is not enabled in the pilot |
| Application logs | Railway | Provider access controls | Configure and verify the account’s log-retention settings before external customer traffic; request bodies, keys and cookies are not logged by application request middleware |
| Infrastructure metrics and alarms | AWS Stockholm | Restricted IAM and independent SNS notification | CloudWatch service retention; email alerts are also retained by the recipient’s mail provider |
| DNS and TLS routing | Cloudflare DNS / Railway ingress | Scoped DNS changes and HTTPS | Provider operational records under their policies |
| Hosted billing | Stripe, plus order/payment references and financial adjustments in PostgreSQL Amsterdam | Stripe-hosted payment entry, scoped roles and verified provider events | Hedvi retains financial records separately from message retention. Card details and raw Stripe event bodies are excluded from the delivery database. |
| Support | Business support systems | Authorized support access | Follow the published support and privacy terms. |

**Deletion.** Removing content deletes its S3 object and clears the database
subject/reference from the email row. Preview and resend then stop working.
Immutable webhook event copies can still contain the subject until organization
metadata retention expires, including when email-content retention is 0. Receiving
webhook systems are controlled by the customer and follow their own deletion policy.
Metadata cleanup removes associated recipients, events and attempts. Retention 0 means deletion after
processing completes, not that content is never stored. Periodic cleanup is not
an instantaneous erasure guarantee.

**Restore.** A restored backup must run retention/deletion reconciliation before
serving users or resuming queues. Existing manual synthetic-test snapshots must
not become an indefinite backup policy for real customer data. Publish any legal
hold and account-deletion exception in the service privacy notice.
