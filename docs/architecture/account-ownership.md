# Account ownership and recovery — implementation contract

Status: implementation under review, 28 November 2026. This is a technical
contract, not adoption of legal terms. It is being developed in an isolated
source snapshot; migrations 1–8 remain the separately deployed baseline.

## Flows

`POST /console/v1/auth/signup` accepts `{email, locale}` and returns the same
202 `{accepted:true}` for new and existing addresses, including address/global
mail-budget exhaustion. It never creates an account, reserves an email address,
sets a password, issues an API key or grants a session. Malformed input remains
422 and the caller's own ingress-IP limit remains 429. Database failure is 503.

The message contains a link to the configured application origin's
`/verify-email#token=<random-secret>`. The fragment is never sent to the HTTP
server. The page removes it from browser history and retains it only in memory.
GET requests never consume it. The recipient explicitly submits a new password,
profile and the current legal acknowledgements. No password supplied by the
requester can be installed: this prevents account pre-hijacking.

`POST /auth/signup/complete` accepts `{token,password,name,organization,consent}`.
Only a valid unexpired signup token may create a user, verified timestamp,
organization, owner membership, default project, onboarding test key, consent
receipts and session. These changes and token consumption are one transaction.
The response retains the existing 201 onboarding result and session cookie.
An existing address is never overwritten or signed in by a signup token.

`POST /auth/password-reset/request` accepts `{email,locale}` with the same 202
contract. `POST /auth/password-reset/complete` accepts `{token,password}`.
Recovery changes the password, proves email ownership, invalidates other
outstanding challenges for that address and revokes every session in one
transaction. It preserves the TOTP secret, enabled flag, replay counter and
staff flag. It creates no session: the user signs in with the new password and
existing MFA. Password reset never serves as MFA recovery.

An unknown reset address is silently discarded by the worker. Existing signup
addresses receive an account-exists message with a recovery link, not a signup
credential. Request handlers do not query account existence, keeping their
response path independent of it. Completion errors may report invalid/expired
links because knowing a 256-bit token is already proof of receiving the mail.

## Proofs, races and transaction boundaries

Tokens contain 32 random bytes, expire after 30 minutes, are single use, and are
stored only as a purpose-separated HMAC-SHA-256 digest. A separate AES-256-GCM
payload binds encrypted address/token data to the immutable outbox UUID through
authenticated associated data. No raw token is stored in an ordinary column,
URL query, metrics, logs, audit metadata or customer email/webhook records.

The worker binds reset tokens to an existing user ID and credential generation
before sending. A token cannot reset a later account reusing the same address.
Password reset increments that generation; login and completion recheck state
under the existing user row lock. Per-address coordination and stable lock
order prevent a queued request from reviving an invalidated earlier token.
Malformed or invalid tokens are rejected before Argon2. Valid completions use
the existing two-operation Argon2 bound and revalidate the token after hashing.

Two concurrent completions have one winner. A lost commit acknowledgement must
not cause account deletion, recreation, token reactivation or compensating
outbox deletion. A consumed link is not replayed to mint another session or
reveal a previously generated API key. The UI directs the user to normal login
if completion may already have succeeded; keys can be recreated there.

## Transactional system-mail outbox

Migration 0009 adds separate authentication challenges, a bounded encrypted
mail outbox, durable mail budgets, ownership timestamps and consent receipts.
Authentication mail never enters the tenant email/credit/webhook pipeline.
The public API inserts the challenge, encrypted message and budget reservation
in one transaction. It gets neither worker DB credentials nor `ses:SendEmail`.

A worker polls and leases jobs using short `FOR UPDATE SKIP LOCKED`
transactions; no database transaction spans SES. At most two worker calls run
concurrently, with 30-second provider timeouts and two-minute leases. Claim and
settlement use attempt ownership checks. A claim whose commit is uncertain is
not sent without confirmation. A worker crash during an attempt or an unknown
SES outcome is terminal `unknown`; no automatic resend. Explicit user resend
is allowed within the same budgets. Provably rejected transient calls receive
bounded backoff and never retry beyond token expiry. Terminal jobs immediately
erase encrypted payloads. Privacy-preserving delivery correlation expires after 90 days; address ciphertext
and user bindings are erased once the proof is no longer usable;
expired challenges and inactive limiter rows are cleaned in bounded batches.

Mail uses fixed, bounded templates and a configured bare sender, SES tenant and
configuration set with SES permanent-bounce and complaint suppression enabled.
The auth configuration set must be distinct from customer event configuration.
It cannot accept user-provided subjects, HTML, sender,
redirects or URLs. Existing `provider.Send` is reusable; dedicated `hedvi_auth`
tags route events to auth-mail handling, not customer webhooks. Permanent
bounces and complaints suppress further account mail to the same address;
suppression never changes the request's outward response. Event processing
requires a matching outbox/attempt/provider-message identity.

Sender configuration uses an operator-owned domain such as `example.com` and
a dedicated operator-managed SES tenant. Ordinary customer keys cannot send
from that reserved domain, and customer domain APIs cannot provision, verify
or delete the domain, its descendants or its ancestors. These are deployment
values, not source defaults.
The operator verifies the SES identity/tenant/configuration-set association and
IAM sender restriction before enabling the feature. System mail cannot create,
take over or reassign a customer sending identity.

## Abuse and storage budget

Ingress uses the already authenticated proxy IP logic. Separate IP and IP/email
limits constrain requests; global and per-address PostgreSQL budgets remain
effective across replicas and Valkey failures. Address/IP keys are HMACs, not
plaintext PII. IPv6 clients share a /64 abuse bucket.

Initial conservative defaults: one address gets at most three messages/hour,
five/day and a 60-second resend cooldown, combined across signup and recovery.
One IP gets ten requests/hour and fifty/day. The installation admits at most
100 auth messages/hour, 500/day and 1,000 pending jobs. Provider retries consume
the same global attempt budget. All limits are checked before creating durable
payload rows; exhaustion cannot create an unbounded collection of expired jobs.
Limits can be deliberately raised in source/configuration after operational
review. Attackers can still consume a victim's resend budget; responses do not
reveal its state, old valid links stay usable, and requests do not extend TTLs.

## Existing accounts and local compatibility

Migration 0009 deliberately leaves existing users unverified and revokes all
legacy sessions and API keys. This disruptive rotation is explicitly approved
for the synthetic pilot. Drain accepted sends before migration; do not silently
re-enable old keys. Existing owners prove their email through recovery and set
a fresh password. Existing MFA is preserved. Users missing their existing
authenticator need separately verified operator assistance, not an email bypass.
Staff grant additionally requires a verified email and the existing independent
human identity check. Login and session loading reject unverified accounts.

Local/mock HTTP uses the same verification flow. Test helpers obtain links from
the isolated worker simulator and then call completion. The local seed and SDK
contract-fixture CLI remain explicit local-only fixtures and may create verified
synthetic accounts. No production environment flag bypasses ownership proof.
A local CLI may show a simulated link; no unauthenticated HTTP inbox is added.

## Legal and web contract

`GET /auth/legal` exposes one canonical embedded manifest: document IDs,
immutable version strings, content digests, URLs and adoption status. Both web
forms and API validation use it. The browser cannot assert a newer/older
version, replace a document URL or omit a required acknowledgement. Signup
records the exact accepted terms/AUP versions and privacy-notice receipt,
timestamp, user and bounded request evidence in append-only records. Privacy
notice receipt is not described as GDPR consent or the processing legal basis.

The canonical six published documents are embedded with their exact byte
digests. Adoption is an operator decision and does not assert counsel review.
The API refuses an unadopted hosted manifest or any missing/mismatched content.
Existing recovered accounts see a legal acknowledgement gate before console
mutations; recovery itself must not be made conditional on accepting new terms.

Web routes: `/signup` (email only), `/verify-email` (password/profile/consent),
`/forgot-password` (generic request result), `/reset-password` (new password),
and `/legal/accept` for an authenticated legacy account. All auth pages use the
existing dynamic nonce CSP, no third-party resources, `no-store` and
`Referrer-Policy: no-referrer`. Messages make no delivery guarantee. The existing
English/Swedish locale choice is retained.

## Required evidence before integration

Use a separate test template/database, never the stable deployment database.
Regression coverage must include: identical new/existing 202 responses; no
account/KDF before proof; pre-hijack attempt; expiry/purpose/single-use/concurrent
completion; password-reset versus login/MFA race; preserved MFA; all-session and
legacy-key revocation; stale credential generation; token/outbox ciphertext
binding; transaction rollback and lost acknowledgements; worker crash/unknown
outcome; atomic cross-replica address/global limits and cleanup; SES-event
suppression; staff restrictions; legal-version mismatches; and no production
local-flow bypass. Run Go race suite/vet and web typecheck/lint/tests/build.

Real SES delivery, IAM restrictions and public proxy behavior require separate
deployment checks. Passing local tests is not certification of the hosted
service, legal adoption or email delivery.
