# Hosted Stripe billing

## Merchant and catalog

Use a separate database, Stripe account or sandbox, and webhook endpoint secret per installation. Confirm the merchant entity, invoice identity, approved markets and Stripe Tax registrations before accepting customers. Select a dedicated customer portal configuration with invoice history and payment-method updates enabled, cancellation at period end with no proration, and customer and subscription updates disabled.

Set the merchant’s public Terms of Service and privacy URLs to the operator’s published documents. Checkout requires an additional terms confirmation. The application’s versioned onboarding acceptance receipt remains the authoritative document record.

Create three active products using the reviewed business SaaS tax code `txcd_10103001`. Each needs two active fixed, exclusive-tax prices. Use the smallest currency unit in Stripe.

| Product | Price type | SEK | USD | Allowance |
|---|---|---:|---:|---|
| Flex | One-time | 39 | 4 | 10,000 recipients and 250 MiB for 365 days from payment |
| Pro 50k | Licensed, every month | 129 | 13 | 50,000 recipients and 1 GiB per paid invoice cycle |
| Pro 100k | Licensed, every month | 249 | 25 | 100,000 recipients and 2 GiB per paid invoice cycle |

Free has 5,000 recipients and 100 MiB per UTC calendar month. A first Flex purchase changes a Free baseline to Flex. Billing members can return to Free in Settings. An active Pro cycle takes priority over the baseline. Prepaid credit beyond a Free or Pro allowance requires the organization's explicit opt-in. New postpaid overage is disabled.

## Configuration

Both the API and worker need these variables.

| Variable | Value |
|---|---|
| `HEDVI_BILLING_ENABLED` | `true` enables billing. Omitted or `false` disables new checkouts and reconciliation. |
| `HEDVI_STRIPE_MODE` | `test` for staging or development, `live` for production |
| `HEDVI_STRIPE_ACCOUNT_ID` | Merchant account ID for this database |
| `HEDVI_STRIPE_PORTAL_CONFIGURATION` | Dedicated portal configuration ID |
| `HEDVI_STRIPE_COUNTRIES` | Explicit comma-separated two-letter billing countries approved by the merchant |
| `HEDVI_STRIPE_PRODUCT_{FLEX,PRO_50K,PRO_100K}` | Three product IDs, one variable per product |
| `HEDVI_STRIPE_PRICE_{FLEX,PRO_50K,PRO_100K}_{SEK,USD}` | Six price IDs, one variable for each product and currency |
| `HEDVI_APP_URL` | Canonical dashboard origin used for Checkout and portal return links |

The API needs `HEDVI_STRIPE_API_KEY` (restricted write key) and `HEDVI_STRIPE_WEBHOOK_SECRET`. During endpoint key rotation it also accepts `HEDVI_STRIPE_PREVIOUS_WEBHOOK_SECRET`. Grant the API permission to read its own account, products, prices and the selected portal configuration, create and retrieve Customers and Checkout Sessions, and create portal sessions. The API key must have the prefix for the selected mode.

The worker and billing admin CLI need `HEDVI_STRIPE_RECONCILE_KEY` (restricted read key). Permit reading its own account, products, prices, Customers including search, Checkout Sessions and line items, Subscriptions, Invoices and invoice lines, Invoice Payments, Payment Intents, Charges, Refunds, Disputes and Credit Notes. Test each resource with the actual restricted keys. Keep all provider keys and webhook secrets out of the web service, images, logs and source control.

Start with migration 0010 applied and a database backup. Start the configured worker and check its first successful account/catalog verification. This pins the account and mode in the database. Enable the API with the same merchant and catalog. Both processes revalidate the six prices and their parent products. A failed remote check pauses billing and retries. Signed reversal admission for a previously initialized installation continues during a Stripe API outage. Existing mail reservations keep their source references. Rolling back to an older application against this migration is unsupported.

## Signed events

Register a direct account **snapshot** destination at `https://<api-host>/billing/stripe/webhook`, API version `2026-11-18.endive`. The SDK is pinned in `apps/api/go.mod`. Select these exact event types. [Stripe webhook configuration](https://docs.stripe.com/webhooks)

- `checkout.session.completed`, `checkout.session.expired`, `checkout.session.async_payment_succeeded`, `checkout.session.async_payment_failed`
- `customer.subscription.created`, `customer.subscription.updated`, `customer.subscription.deleted`
- `invoice.paid`, `invoice.payment_failed`, `invoice.payment_action_required`, `invoice.finalization_failed`, `invoice.voided`, `invoice.marked_uncollectible`
- `charge.refunded`, `refund.created`, `refund.updated`, `refund.failed`, `charge.dispute.created`, `charge.dispute.updated`, `charge.dispute.closed`
- `credit_note.created`, `credit_note.updated`, `credit_note.voided`

Hedvi authenticates the original body, deduplicates the event ID and retains identifiers used for reconciliation. The webhook transaction suspends a recognized source on an adverse event before responding. It stores no card, billing address, tax identifier or raw event body. Verified canonical invoice, item, price, customer, captured card payment, taxes and reversal evidence determine grants. A browser payment return only reads the order status. [Stripe invoice object](https://docs.stripe.com/api/invoices/object)

## Payment outcomes and recovery

- A paid Pro invoice grants its recurring line's exact service interval. Failed or unpaid renewal does not open a new interval. End-of-period cancellation preserves a paid interval. To change Pro tiers, cancel in the portal, wait until the subscription ends, then buy the other tier.
- A full refund or lost dispute permanently revokes the original source. A partial refund or an unresolved credit note/dispute holds remaining spending. A verified won dispute, canceled or failed refund, or voided credit note can restore spending if all other evidence still matches. A previous paid-but-revoked period may be replaced by a separately verified payment. Two usable intervals cannot overlap.
- Existing reserved sends can finish. Settlement always charges or releases their original reservation. Keep paid invoices, held usage periods, lots, ledger entries and adjustment history intact when investigating.
- A billing country mismatch can reach support **after Stripe has collected payment**. Checkout can collect a billing address different from the country the buyer selected in Hedvi. Hedvi holds the order, creates an alert and grants no service. Review the actual charge and address in Stripe, then refund an unsupported sale through the merchant's controlled process. If collection itself is limited by country or business eligibility, gate hosted Checkout through the merchant's approved onboarding/payment controls and prove the rejection before enabling public sales. Tax ID collection alone does not establish eligibility.
- Billing recovery warnings appear in the normal organization alerts. After three consecutive read failures the worker records an alert and retries with increasing delays. Inspect merchant, mode, restricted permissions, country, tax totals, price, checkout/customer bindings and payment/refund evidence in Stripe. Resolve credentials or merchant configuration in the secret store.
- Recheck one resource with `hedvi admin billing-reconcile -org <org-uuid> -ref <order-uuid-or-invoice-id> -note "Reason for the review"`. This records an audit entry and makes the worker fetch canonical facts. An order-level recheck resets its discovery cursor. A missing or conflicting payment remains unavailable. Do not repair by changing the paid source or rewriting reservations. For a deliberately agreed partial goodwill allowance, use a separately audited manual credit with a unique support case reference after reviewing usage and outstanding reservations.
- A disabled installation stops consuming provider events. Keep the webhook receiver and worker enabled while valid purchased entitlements exist. For an incident suspend new checkouts at ingress while continuing webhook delivery and reconciliation.
- Active subscriptions poll with a persisted creation-time cursor and a 48-hour overlap. Open invoices remain independently tracked. Completed Flex checkouts and ended subscriptions use event or manual wakeups. Usable grants are rechecked daily until their service window ends. Retain the minimal billing identifiers and adjustment journal under the installation's documented financial retention policy, separate from message-content retention.

## Sandbox verification

Use a separate database and the merchant's sandbox. Exercise both currencies, one-time Flex, both Pro products, the configured tax registrations, first and subsequent invoices, payment failure, period-end cancellation, new purchases after termination, refunds/disputes, delayed and duplicate webhooks and role revocation. Verify Checkout totals, invoice lines and scoped ledger changes together. Compare accepted sends across a billing boundary with their original source. Check point-in-time restore against replayed events before enabling live keys. [Stripe Billing tests](https://docs.stripe.com/billing/testing)
