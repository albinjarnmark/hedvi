# Provider send incidents

The send worker tracks an incident separately for each project, immutable
domain claim, envelope From domain, SES tenant and configuration set. A refusal
on one path cannot suppress a new alert on another path, and acceptance on one
path cannot close another path's incident. Alert messages contain a fixed safe
description and an error code, never the raw provider error.

Before calling SES, the worker commits the attempt and its path binding.
`provider_send_routes` stores the newest observed attempt's database start time,
UUID tie-breaker and outcome. Updating this watermark and opening or resolving
the alert happens in the same transaction as the attempt's settlement. A
delayed older refusal cannot reopen an incident after a newer accepted attempt;
a delayed older acceptance cannot close an incident established by a newer
refusal. Retries in an open incident deduplicate. A refusal after confirmed
recovery opens a new incident and becomes eligible for a new operations alert.

Only a direct successful SendEmail response with a provider message ID proves
recovery. Throttling, unknown outcomes, manual resolution and asynchronous SES
feedback do not claim that the current provider path is healthy. This is
deliberately conservative: late feedback proves historical acceptance, not that
the latest permissions/configuration are working. Legacy attempts without a
recorded path also cannot claim recovery. The alert tracks
provider access/configuration for this domain path; it is not a delivery or
recipient-health guarantee. Deployments with mailbox-specific IAM conditions
must investigate those conditions separately before treating a domain-path
acceptance as broader evidence.

If recording the watermark or alert fails, the attempt's result transaction
rolls back. A confirmed provider call is not repeated automatically: the
durable started attempt follows the existing lease expiry, unknown-outcome and
SES reconciliation rules. Reservations, idempotency and retry deadlines retain
their existing behavior. Database errors must not be swallowed to manufacture
a successful incident transition.

## Retention and access

The worker alone can read or mutate `provider_send_routes`. The public API
cannot change send attempts or the watermark. Route rows contain identifiers,
the sending domain, tenant/configuration names and attempt ordering; they do
not contain message content, recipient addresses or sender mailbox local parts.

Watermarks survive email/attempt retention and the 90-day retention of resolved
alerts. They remain for the lifetime of the referenced project/domain record
and cascade on its hard deletion; a customer soft deletion does not erase the
ordering evidence. Do not independently expire a watermark while old attempts
could still settle. Each configured send path has one row, rather than one row
per request. Changing a configuration or recreating a claim creates a separate
path and does not assert recovery for the retired path. Review retired-path
alerts explicitly if no later send will use that path.

## Rollout

Apply migration 0015 before starting the new worker. Attempts created before
that migration have no trustworthy path snapshot and continue settling under
the previous delivery rules, without changing scoped incident state. Their
ordinary retries create a new bound attempt.

Old global `provider:<code>` alerts are left open for explicit operator review:
they cannot safely be attributed to a single path. New `provider-send:<hash>`
keys are independent, so those old rows cannot suppress a new incident. After
checking the affected paths, close the legacy alert with an audit note; do not
interpret the migration or one unrelated success as proof of global recovery.

Operations notification still uses the existing durable pending-alert flow.
Retries after an uncertain SNS publish can repeat a notification; alert IDs
identify the same incident. There is no exactly-once external notification
claim.
