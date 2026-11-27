# Hedvi

**Email, without the overhead.** A transactional email API designed to make email easier to test, understand and pay for. Licensed under AGPL-3.0-only. Scope and known release limits: [`docs/status.md`](docs/status.md).

> Pre-release source. Local development uses an SES/SQS simulator. A controlled AWS/Railway pilot has exercised real delivery, bounce/complaint handling and signed webhook retries. Public hosted-service launch has separate requirements; see the status document.

## Repository

| Path | What |
|---|---|
| `apps/api` | Go: public API (Resend-compatible subset), console API, workers, migrations, admin CLI |
| `apps/web` | Next.js 16: hedvi.dev (en) + /sv, docs, dashboard (app.hedvi.dev) |
| `contracts/` | Pinned Resend SDK contract tests (Node 6.39.0, Python 2.54.0) |
| `infra/terraform` | AWS eu-north-1: payloads, queues, SES events, IAM, alarms, state bootstrap |
| `docs/` | Architecture policies, data map, compatibility, operations and runbooks |
| `brand/`, `third_party/` | Editable brand assets and third-party license inventory |

## License and participation

The platform is [AGPL-3.0-only](LICENSE). You can inspect, modify, self-host and
commercially use it under that license. Modified network-served versions must
offer their corresponding source to their users. Calling the HTTP API does not
by itself make your application part of Hedvi. Customer integration snippets
also have an [MIT permission](docs/INTEGRATION-EXAMPLES-LICENSE).

See [contributing](CONTRIBUTING.md), [security reporting](SECURITY.md),
[trademarks](TRADEMARKS.md), [third-party notices](THIRD_PARTY_NOTICES.md) and
[self-hosting](docs/self-hosting.md). StackX's managed service supplies operation
and support; self-hosting has its own infrastructure costs and responsibilities.

## Run locally

Requires Docker, Go 1.26, Node 22+ with pnpm 10, Python 3.10+.

```bash
make up        # PostgreSQL :55432 and Valkey :56379 (local only)
make migrate
make seed      # demo@hedvi.test / hedvi-local-demo, prints test and live keys
make dev       # API + workers on :8780 with the SES/SQS simulator
pnpm install && make web   # http://localhost:3030
```

Send a test email:

```bash
curl -X POST http://127.0.0.1:8780/emails -H "Authorization: Bearer hv_test_…" \
  -H 'Content-Type: application/json' \
  -d '{"from":"Acme <onboarding@hedvi.dev>","to":"you@example.com","subject":"Hi","html":"<p>Hi</p>"}'
```

With the seeded live key and `from: app@demo.hedvi.test` the simulator "sends" and emits SES events. Recipient patterns `+bounce`, `+complaint`, `+delay`, `+suppressed` simulate outcomes; header `X-Hedvi-Simulate: unknown_after_accept|unknown_before_accept|reject|throttle` forces provider faults.

## Test

```bash
make test-go     # unit + integration (crash, quota race, RLS isolation, events, webhooks); needs `make up`
make test-web    # typecheck, lint, unit tests
make contracts   # SDK contracts against a running `make dev`
```

## Principles

PostgreSQL is the source of truth (Valkey only rate-limits). An email is accepted only after one transaction stores it with recipients, quota reservation, outbox job and idempotency record. Ambiguous provider outcomes become `delivery_unknown` and are never retried blindly. Test keys can never reach SES (enforced in the database). Tenants are isolated by row level security. See [`docs/architecture/acceptance.md`](docs/architecture/acceptance.md).
