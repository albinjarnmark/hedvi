# Resend compatibility matrix

Pinned and contract-tested: **resend-node 6.39.0** (`RESEND_BASE_URL`), **resend-python 2.54.0** (`RESEND_API_URL`). See `contracts/`. Other SDKs or versions are not promised until tested.

| Area | Status | Deviation |
|---|---|---|
| `POST /emails` from, to, cc, bcc, reply_to, subject, html, text, headers, tags, attachments (base64) | Supported | Max 50 recipients, 10 attachments, 10 MB total; structural headers refused; executables refused |
| attachments `path` (remote URL) | 422 | No remote fetching (SSRF) |
| `scheduled_at`, `template`, `topic_id`, `react` (raw) | 422 | Not silently ignored |
| `Idempotency-Key` | Supported | 24 h, scoped org/project/operation/test-or-live-mode, replay header `Idempotent-Replayed` |
| `GET /emails/{id}` | Supported | `last_event` adds `delivery_unknown`, `test_captured`; `hedvi` object added |
| `GET /emails` (limit/after/before) | Supported | Limited to the key’s mode and sender-domain scope |
| `POST/GET/DELETE /domains`, `POST /domains/{id}/verify` | Supported | Requires an unrestricted full-access live key. Region is `eu-north-1`. Optional values are `tls: opportunistic`, `capabilities.sending: enabled` and `capabilities.receiving: disabled`. The first response supplies an ownership TXT. Verification after live access approval supplies the remaining sending records. |
| Error shape `{statusCode,name,message}` | Supported | Same names |
| Rate limit headers | Supported | 10 req/s per key and organization |
| Webhook payloads and signatures | Compatible | Standard Webhooks headers + `svix-*`; per-recipient `to`; Hedvi events prefixed `hedvi.` |
| Batch, PATCH/cancel email, API keys API, webhooks API, audiences, contacts, broadcasts, templates, receiving, metrics | Explicit 404 | Planned only after demand |
| Open/click tracking | Not supported | Off by default; needs privacy assessment |
| SMTP | Not yet | Beta if design partners need it (`250` only after durable acceptance) |

## Send request validation

| Input | Limit |
|---|---|
| From, To, Cc, Bcc, Reply-To | Each input is at most 1,024 bytes and each decoded display name at most 128 bytes. Local parts use unquoted ASCII dot-atoms of up to 64 bytes. IDNA-normalized addresses are at most 254 bytes. |
| To, Cc, Bcc | Up to 50 entries combined, counted before deduplication. Reply-To allows up to 50 additional entries. |
| Subject | Up to 989 bytes of UTF-8. Header values and address fields reject control characters. |
| Custom headers | Up to 30 unique, case-insensitive names. Names allow up to 76 printable ASCII bytes excluding colon. Name, colon, space and value together allow 998 bytes. Structural headers and the prefixes `X-SES-`, `X-AMZ-`, `X-Hedvi-` and `Resent-` are reserved. This includes authentication results, SPF results, error-routing and read-receipt overrides, and MIME content ID, location and description. |
| Attachments | A plain filename up to 255 bytes, with executable extensions, control characters and trailing periods rejected. A supplied `content_type` must be a valid MIME type/subtype of at most 255 bytes including parameters. |

Messages already queued with a newly reserved custom header fail validation when the worker loads the stored payload. Review affected pending messages when upgrading.

Each JSON request contains one object. Unknown fields and trailing JSON return a validation error. The domain ownership and approval workflow is documented in the [domain guide](architecture/domains.md).

## Operational resource limits

API requests share a 10 requests/second token bucket (burst 20) across all keys
and projects in an organization, in addition to the per-key bucket. Valkey shares
this limit between replicas; a Valkey outage falls back to per-process limits.
PostgreSQL remains authoritative for durable resource and sending budgets. Before a key lookup, an IP bucket permits 50 requests/second with a burst of 100.

Each project allows 10 webhook endpoints, 20 active API keys and at most 50 new
keys in a rolling 24-hour period (revoked keys still count toward creation).
Production webhook endpoints use HTTPS on port 443 or 8443.

Project hourly/daily admission limits include all unique requested recipients,
including suppressed addresses. Commercial sending quota remains billable-only.
Retained live payloads have a separate default 1 GiB/project capacity, enforced
atomically in PostgreSQL and released by content deletion. A reviewed operator
may adjust the capacity; plan attachment allowances remain separate.

Console email resends accept an `Idempotency-Key` scoped to the original message. A retry with the same key replays the accepted copy for 24 hours. Clients omitting the header share a default key per user and original message for that period. The resend link and audit entry commit with the new email.

A 500 response or lost connection can have an uncertain acceptance outcome. Retry
with the same idempotency key and identical payload within its 24-hour lifetime.
Without that key, or after it expires, inspect the email log before resending.
