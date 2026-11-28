# Account verification and recovery

This feature requires migration 0009 and the API, web and worker release that
uses it. Never enable an older immediate-signup API against the new database.

## Operator sender

Provision a dedicated SES identity on an operator-controlled subdomain such as
`auth.example.com`, a separate operator tenant and an auth-only configuration
set. Enable SES permanent-bounce and complaint suppression for this set and
route its events into the existing authenticated SNS/SQS event path. Configure:

```
HEDVI_AUTH_MAIL_FROM=accounts@auth.example.com
HEDVI_AUTH_MAIL_TENANT=operator-auth
HEDVI_AUTH_MAIL_CONFIGURATION_SET=operator-auth-events
HEDVI_APP_URL=https://app.example.com
HEDVI_LEGAL_ORIGIN=https://example.com
```

These resources must never belong to an ordinary Hedvi project. Exclude the
reserved identity, configuration set and tenant from the public API principal's
SES create/tag/associate/delete permissions, including parent-domain takeover
paths. The API still gets no SendEmail action and no worker DB credentials.
The worker uses the pre-provisioned resources; it does not create or reassign
them. Confirm the effective IAM principal, identity verification, resource
associations and suppression options before accepting external registrations.

## Planned pilot upgrade

1. Stop new API requests and drain accepted sends. Stop workers. Record any
   unresolved send attempts and settle them using the existing uncertain-send
   runbook. Never use migration 0009 to imply that an unresolved send was safe.
2. Apply migration 0009 with only the migration database credential. It leaves
   all existing account emails unverified and revokes all existing sessions and
   API keys. This is an intentional rotation for the synthetic pilot.
3. Deploy matching API/web/worker builds, canonical legal documents and manifest.
   Configure the operator sender resources above. Existing users request a
   password-reset email, choose a new password and sign in with their existing
   authenticator. Staff grants still require verified email, MFA and the separate
   human identity check. Reissue required pilot API keys after ownership proof.
4. Test signup, duplicate signup, reset, replay rejection, MFA, old-key rejection,
   reserved-domain protection and delivery feedback through actual ingress/SES.
   Confirm only worker-side system mail sends and inspect queue/DLQ/alerts.

Do not roll back to code that omits ownership/consent checks. If the new release
fails, close account endpoints and repair forward. Do not re-enable old keys or
mark all users verified to bypass an operational problem.

## Legal versions

The canonical content lives in `apps/web/src/content/legal/*.json`. After the
operator finalizes the texts, run `python3 scripts/sync-legal.py --version
<new-version> --adopted`. This records adoption of the hosted-service documents;
it makes no statement that counsel reviewed them. Each adopted version is
immutable. Run `python3 scripts/sync-legal.py --check` in CI; Go also checks the
embedded bytes against their digests. Both registration and the legacy-user
gate record these exact document versions. Recovery and MFA controls remain
available without accepting changed terms.

## Operational behavior and retention

Requests return generic 202 responses; these do not confirm an account exists
or guarantee an email. Per-address limits combine signup and recovery. The
initial limits are three messages/hour, five/day and 60 seconds between requests;
per-IP limits group IPv6 /64s. Installation limits are 100 admitted messages/hour,
500/day, 1,000 physically pending jobs and two active provider attempts. Attempts
also consume a global budget, including safe retries. Investigate abuse before
raising limits.

Proofs expire after 30 minutes. Unknown sends and expired in-flight leases are
never automatically retried. Requesting another link is deliberate and budgeted.
Terminal delivery removes the encrypted token payload; invalid/expired queued
payloads are removed by the worker. Worker outages can delay cleanup, subject
to the physical queue ceiling. Provider errors create metadata-only alerts.

Address ciphertext and user bindings are cleared when proofs are consumed or
invalidated, or by expiry cleanup. Pseudonymous address HMACs, token hashes and
minimal job/attempt/provider-ID correlation remain for at most 90 days to bind
late feedback; hashes are not anonymous data. Inactive HMAC IP/address budgets
are cleared after two days. Hedvi’s application-level permanent-bounce block lasts 90 days. Application-level
complaints and provider-level bounce or complaint suppressions can remain until a
separately reviewed removal. Dedicated SES suppression
provides another layer for feedback arriving after application correlation has
expired. Never apply unbound event tags to an address.

Legal receipts retain user, document versions/hashes, time, IP and a bounded
User-Agent while the account exists; account deletion cascades to these receipts.
Do not log tokens, links, encrypted payload plaintext or raw provider error
messages. Inspect job IDs, safe result codes and aggregate counts.

## Local use

Local/mock mode exercises the same HTTP ownership flow. After requesting a
message with the local worker running, use `hedvi admin auth-mail-link -email
person@example.test` to print its simulated link. This command is refused in
deployed environments. It is not an HTTP inbox. `seed-local` and SDK contract
fixtures remain explicitly local synthetic fixtures.

Integration suites may set
`HEDVI_TEST_TEMPLATE=hedvi_auth_release_test_template` to avoid replacing the
default test template used by another development task. Only disposable local
test databases are permitted for fault and migration regressions.

Legal links resolve against `HEDVI_LEGAL_ORIGIN` (defaults to `HEDVI_APP_URL`) and use immutable `/legal/versions/<manifest-version>/<document>` paths. Receipt rows retain the origin presented at acknowledgement alongside canonical versioned paths and JSON digests. Changing the public hostname alone does not invalidate an unchanged document acknowledgement. Keep archived legal JSON and routes when adopting future versions; `scripts/sync-legal.py --check` checks the current archive too. For Hedvi's domain, set the app origin to `https://app.hedvi.dev` and legal origin to `https://hedvi.dev`; self-hosters use their own origins.

The hosted instance owner uses a second authenticator for privileged operations. Activation, factor recovery and revocation require the private operator procedure in the [instance owner runbook](hosted-owner.md).
