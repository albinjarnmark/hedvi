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

Migration 0018 removes hourly/daily plan caps for usable active Flex and Pro allowances. Free retains 50/hour and 200/day, including when the customer selects Free as their baseline while retaining unused Flex credits. A hold or expiry removes that allowance from the calculation. The API rechecks limits under the organization lock. Existing counters, pauses and specific owner exceptions survive membership changes.

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

Start with migrations 0010 and 0011 applied and a database backup. Start the configured worker and check its first successful account/catalog verification. This pins the account and mode in the database. Enable the API with the same merchant and catalog. Both processes revalidate the six prices and their parent products. A failed remote check pauses billing and retries. Signed reversal admission for a previously initialized installation continues during a Stripe API outage. Existing mail reservations keep their source references. Rolling back to an older application against this migration is unsupported.

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
- The organization supplies its company billing name and complete invoice address before Checkout. The API validates the configured country, binds an immutable profile in PostgreSQL, creates the Customer with `tax[validate_location]=immediately`, and checks the returned and subsequently retrieved address. Checkout uses `customer_update[address]=never`, `billing_address_collection=auto`, and no shipping address. Existing shipping information causes a pre-checkout rejection because it can override the saved tax location. The tax-ID flow can still update the legal business name, but cannot change this invoice address. [Stripe Checkout address priority](https://docs.stripe.com/tax/checkout/page)
- Invoice-country eligibility is separate from card issuance and payment-method billing details. A foreign-issued card or a different payment-method address does not change the saved company invoice address. The worker still requires the canonical paid invoice's country to match the bound order and allowed markets, with complete automatic tax and all existing payment, price, consent and reversal evidence. An unexpected invoice country still holds service and raises an alert for reconciliation; this is defense in depth, not a substitute for the pre-payment address control.
- A profile is fixed once Customer creation succeeds or has an uncertain outcome. A PostgreSQL lease serializes creation across API replicas without keeping a transaction open during Stripe I/O. Only HTTP 400 `customer_tax_location_invalid`, without a retry instruction, from the first exclusive attempt makes the profile editable; no local order or Checkout exists yet. Correcting it rotates the attempt UUID and idempotency key. Other errors, expired leases, crashes and transport timeouts permanently mark that attempt uncertain. A later address rejection or stale response cannot unlock it. The UI reloads the saved/editable state after an error. Uncertain retries reuse the exact Customer parameters and key within the existing 23-hour window; after that, request operator reconciliation before another creation. Checkout retries retain their existing durable order/idempotency rules.
- Stripe tax-location validation establishes that an address can be used to calculate tax; it does not prove the company's real-world identity or residence. Tax-ID collection does not synchronously establish business eligibility. [Stripe customer locations](https://docs.stripe.com/tax/customer-locations), [tax-ID validation](https://docs.stripe.com/tax/checkout/tax-ids)
- Billing recovery warnings appear in the normal organization alerts. After three consecutive read failures the worker records an alert and retries with increasing delays. Inspect merchant, mode, restricted permissions, country, tax totals, price, checkout/customer bindings and payment/refund evidence in Stripe. Resolve credentials or merchant configuration in the secret store.
- Recheck one resource with `hedvi admin billing-reconcile -org <org-uuid> -ref <order-uuid-or-invoice-id> -note "Reason for the review"`. This records an audit entry and makes the worker fetch canonical facts. An order-level recheck resets its discovery cursor. A missing or conflicting payment remains unavailable. Do not repair by changing the paid source or rewriting reservations. For a deliberately agreed partial goodwill allowance, use a separately audited manual credit with a unique support case reference after reviewing usage and outstanding reservations.
- A disabled installation stops consuming provider events. Keep the webhook receiver and worker enabled while valid purchased entitlements exist. For an incident suspend new checkouts at ingress while continuing webhook delivery and reconciliation.
- Active subscriptions poll with a persisted creation-time cursor and a 48-hour overlap. Open invoices remain independently tracked. Completed Flex checkouts and ended subscriptions use event or manual wakeups. Usable grants are rechecked daily until their service window ends. Retain the minimal billing identifiers and adjustment journal under the installation's documented financial retention policy, separate from message-content retention.

## Sandbox verification

Use a separate database and the merchant's sandbox. Exercise both currencies, one-time Flex, both Pro products, the configured tax registrations, first and subsequent invoices, payment failure, period-end cancellation, new purchases after termination, refunds/disputes, delayed and duplicate webhooks and role revocation. Verify Checkout totals, invoice lines and scoped ledger changes together. Compare accepted sends across a billing boundary with their original source. Check point-in-time restore against replayed events before enabling live keys. [Stripe Billing tests](https://docs.stripe.com/billing/testing)

## Address-policy cutover and corrections

Before enabling the new creation path, suspend new checkout requests at ingress while keeping webhooks and reconciliation running. Inventory all existing merchant Customers, open Checkout Sessions and active subscriptions belonging to this installation. Expire every old payable session created with automatic Customer address updates; disabling the Hedvi button alone does not invalidate its Stripe URL. Reconcile completed payments and renewals. Migration 0011 deliberately leaves legacy customer profiles NULL and the API refuses a new purchase until an operator has reviewed that customer; it does not infer an address from an earlier dropdown or silently alter a subscription's invoice address.

Do not hand-edit an established profile or Customer address while any linked session can complete or subscription can renew. Definitively rejected first-attempt addresses can be corrected in the form without support. For an uncertain first attempt, first reconcile by the saved `hedvi_customer` metadata and the order reference, including ambiguous attempts and Stripe's idempotency-retention window. If there is conclusively no Customer, Checkout Session, subscription, invoice or entitlement, a reviewed maintenance migration can remove the unused local customer attempt and let the buyer enter a corrected profile. Record the reason and evidence in the support case. If any external or financial source exists, preserve its identifiers and history and prepare a separate reviewed account/address migration; self-service address changes are not part of this version.

Restrict manual Stripe Customer edits to this process. The API detects address/shipping drift before returning a new or reused URL, but it cannot revoke a URL already handed out or intercept a subscription renewal after an administrator changes Stripe directly. Current Stripe behavior can finalize subscription invoices without automatic tax when the customer's location becomes invalid, so address drift is an operational incident, not something to repair after the next charge. [Stripe finalization behavior](https://docs.stripe.com/tax/customer-locations)

Before live release, verify in the actual Stripe sandbox that an SE company profile remains the invoice and tax address when payment-method details change. Cover Flex and both Pro tiers, SEK/USD, foreign-issued cards, card replacement through the portal and a subsequent Pro invoice. Confirm the invoice's saved address and tax totals, not just the Checkout success page. Also prove that expired old URLs cannot be paid and that unsupported/missing company countries cannot create a session. The local HTTP fixture suite checks request and reconciliation invariants; it does not emulate Stripe's real hosted UI or tax engine.
