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

**Live acceptance windows.** The normal Free limit is 50 recipients per hour and 200 per day. Available Flex credits allow 100/1,000. Current usable Pro 50k and Pro 100k invoice or complimentary periods allow 250/2,500 and 500/5,000 respectively. Every project shares the same organization's counters. A lower owner exception limits its project; a higher one permits that project to use its reviewed ceiling against the same shared count. Other projects keep their own ceilings. Switching plans or projects does not clear counters. UTC hour and calendar-day windows count all admitted recipients, including suppressed ones. They cap acceptance into the queue; SES enforces separate regional sending quotas. Test mode allows 500 messages/day and 50 MB stored per project.

Migration 0017 preserves current non-default project limits. Historical ordinary approvals submitted 25/100 even when the owner used the form defaults. Those projects follow the plan unless an earlier recorded approval set different limits before a return to 25/100. Existing pauses remain in force. Apply the migration before rolling out the API and worker together. The owner console can save an explicit limit, including 25/100, or return a project to its plan.

The billing runbook describes invoice verification, refund holds and operator reconciliation. [Hosted billing](../runbooks/billing.md)
