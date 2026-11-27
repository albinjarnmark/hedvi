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

**Rate windows.** New live projects: 25 recipients/hour and 100/day (raised by staff after review). Test mode: 500 messages/day and 50 MB stored per project, separate from live quota.

The billing runbook describes invoice verification, refund holds and operator reconciliation. [Hosted billing](../runbooks/billing.md)
