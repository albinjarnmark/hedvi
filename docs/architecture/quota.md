# Quota unit, reservation lifecycle and credits

**Unit.** One unique delivery recipient across To, Cc and Bcc. Addresses are normalized (IDNA domain, lowercase) and deduplicated with priority To → Cc → Bcc, so a Bcc address that also appears in To is delivered once and never exposed. Suppressed recipients are accepted for visibility, not charged and not sent. At most 50 recipients per email.

**Order of sources** (in the acceptance transaction, serialized per organization by locking the period row):
1. The exact paid subscription interval, or the organization's UTC-calendar baseline when no paid interval applies.
2. Flex credit lots, nearest expiry first — always on Flex, on other plans only when the customer enables it.
Otherwise **hard stop** before acceptance (`429 monthly_quota_exceeded`): nothing sent, nothing charged.

**Lifecycle per recipient** (`recipients.billing_state`): `reserved` → `consumed` exactly once when SES accepts; `reserved` → `released` exactly once when the email fails or is cancelled unsent. Bounces and complaints after acceptance are not refunded. Unknown outcomes stay reserved until reconciled. Every movement is appended to `credit_ledger`.

**Period change.** A job reserved in one month settles in that month's period, even if it is sent after the month changed. Unused monthly quota does not roll over.

**Credit expiry.** Purchased lots are valid 365 days from the verified payment. At expiry the unreserved remainder expires. Units reserved before expiry are honoured if sent; if released later, they expire instead of returning. A reservation never extends validity (`TestCreditsExpireWithoutExtensionByReservations`).

**Data budget.** Attachment bytes (base64 as sent) × recipients count against the monthly data budget (Free 100 MiB, Pro 50k 1 GiB, Pro 100k 2 GiB and Flex 250 MiB per lot). These are beta starting values to be cost-tested.

**Warnings.** At 80 % and 95 % of included quota an alert is shown in the dashboard and `hedvi.quota.warning` is sent to subscribed webhooks — channels that keep working when email quota is exhausted.

**Live acceptance windows.** Free allows 50 recipients per hour and 200 per day. Usable Flex credits and current usable Pro invoice or complimentary periods have no hourly/daily plan cap. In the internal console contract, zero means no plan cap; a negative window preserves a historical explicit manual zero stop. Explicit operator restrictions remain project-scoped and cannot be bypassed by buying credits. For Free, a raised exception affects only that project. All projects still share organization counters and commercial reservations. Switching plans does not clear counters, and expired/held/exhausted credit entitlements do not grant paid throughput. A selected Free baseline retains Free limits even with unused Flex credits. UTC windows count all admitted recipients, including suppressed ones. Monthly/credit and attachment budgets, payload storage, API rate limits, abuse holds and suppression are unchanged. Test mode allows 500 messages/day and 50 MB stored per project.

**Suppression work protection.** A separate durable organization counter allows up to 10,000 suppressed-recipient events per UTC day. Mixed requests count their suppressed recipients. Late suppressions always finish safely and release commercial reservations, even above the threshold; further live admission then returns `suppression_rate_exceeded` until the next UTC day. Replay is free. This bounds processing that does not consume sending credits and does not impose a cap on ordinary paid sending. Counters cannot be reset by the API role and are retained for two days.

**Provider pacing.** Workers serialize submissions through a worker-only database clock per environment/configuration set. A message waiting for capacity retains its reservation, ID and deadline without recording a provider attempt or increasing retry backoff. Requests already accepted remain queued. `HEDVI_SES_SEND_RATE` defaults to 10 recipients/second in production and 2 in staging; all replicas in an environment share this budget. Operators must allocate the SES account's regional rate across environments and account email, and raise it only after AWS approves quota. SES's rolling 24-hour quota remains a separate provider constraint. Definitive throttles retry safely; ambiguous outcomes are never automatically resent.

**Rolling deployment.** Migration 0018 adds `hedvi_live_limits_v2` and the pacing table without changing v1. Migrate first, deploy and drain all old workers first, then deploy API/web, then verify the effective entitlement and a real send. The previous binary keeps its original limits if rolled back.

Migration 0017 preserves current non-default project limits. Historical ordinary approvals submitted 25/100 even when the owner used the form defaults. Those projects follow the plan unless an earlier recorded approval set different limits before a return to 25/100. Existing pauses remain in force. Apply the migration before rolling out the API and worker together. The owner console can save an explicit limit, including 25/100, or return a project to its plan.

The billing runbook describes invoice verification, refund holds and operator reconciliation. [Hosted billing](../runbooks/billing.md)
