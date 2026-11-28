# Instance owner console

The instance owner can review recent signups and operational incidents, issue complimentary memberships, and resolve sending restrictions. Each installation has one owner. The owner opens **Owner console** at `/app/admin` on the authenticated app host.

## Release and private networking

1. Check the number of existing staff accounts with the operator connection using `SELECT count(*) FROM users WHERE is_staff`. Migration 0012 enforces a maximum of one. If several accounts are flagged, identify the installation owner through an established independent channel and resolve obsolete staff assignments under an audited change before retrying the migration. A pre-existing flag still requires a fresh bootstrap.
2. Apply migrations through 0014 with the migrator role before starting the new API or worker. Schedule an appropriate write window for indexes on users, projects, alerts and delivery history. Retain the migration log and backup reference.
3. On the worker set `HEDVI_ADMIN_ADDR=[::]:8791` for a private IPv6-capable service network, or bind the equivalent private interface. Leave the variable empty to keep the listener off. Configure private network access from the web service, with public ingress to the worker disabled. For an ECS deployment allow the web security group to reach the worker security group on TCP 8791 only.
4. On the web service set server-side `HEDVI_ADMIN_INTERNAL_URL=http://<private-worker-host>:8791`. Keep `HEDVI_API_INTERNAL_URL` pointed at the ordinary API. Set `HEDVI_APP_HOST` to the canonical app host. Both Go services need the existing environment's app URL, allowed console origins, `HEDVI_COOKIE_SECURE=true`, matching data key and pepper. Keep the worker database URL on the worker. The public API checks its restricted role at startup.
5. Verify that the app-host unlock endpoint requires authentication, the public API returns 404 for hosted owner routes, and the worker has only private ingress. Use synthetic accounts in staging to check that an ordinary account cannot open the console. Inspect the actual app-host response headers after an authorized unlock to confirm Secure, HttpOnly, SameSite=Strict and a 30-minute lifetime on the owner cookie.

## Appoint or recover the owner

Have the intended owner create an ordinary account, complete email verification and the legal acknowledgement, then enroll an authenticator in their profile. Verify the human, normalized account email and exact user UUID through an established independent channel. Record a concise verification reference suitable for an audit log. Keep credentials and identity documents out of the note.

On a trusted operator workstation, prepare a separate authenticator enrollment file. Set its path inside a private, encrypted directory outside the checkout and replace the environment, UUID and email below with independently verified values. The helper creates a new owner-only file and refuses to overwrite it.

```sh
python3 scripts/owner-factor.py prepare --environment production --user '<verified-user-uuid>' --email '<verified-address>' --file '/private/operator-directory/owner-enrollment.json'
```

On the verified owner's authenticator, keep the existing **Hedvi** entry for ordinary sign-in. Import the file's provisioning URI into the owner's trusted authenticator as **Hedvi Owner (production)**. For manual setup, enter the file's separate base32 secret and select a time-based SHA-1 code with six digits and a 30-second interval. Staging uses **Hedvi Owner (staging)**. Transfer enrollment material only through the private operator channel. Keep it out of browser requests, issue trackers, chat, deployment variables and logs.

With the private operator configuration loaded and an approved route to the installation's worker database, run the pipeline in a trusted local terminal. It prompts for the first code from the new owner entry without echo. The helper transmits the bounded activation envelope only through a pipe. Run the CLI where standard input and process execution are private and the environment matches the enrollment file. The verification note should identify the independent check without including the secret or a code.

```sh
python3 scripts/owner-factor.py activate --file '/private/operator-directory/owner-enrollment.json' | hedvi admin make-staff -user '<verified-user-uuid>' -email '<verified-address>' -identity-verified -owner-factor-stdin -note '<independent identity check and reference>'
```

The transaction validates the first independent owner code, rejects reuse of the ordinary sign-in secret, binds both enrollments to the verified UUID and email, invalidates prior sessions, and records the appointment. The encrypted owner secret is stored in a worker-only table. After verifying activation, remove the provisioning file from the workstation or transfer it into the approved operator recovery vault. A browser account cannot appoint another owner. To transfer ownership, revoke the original owner first. To rotate a lost authenticator, revoke the binding, follow the installation's independently verified account recovery procedure, then verify the new enrollment before appointing the owner again.

```sh
hedvi admin revoke-staff -user '<previous-owner-uuid>' -note '<verified reason and change reference>'
```

After bootstrap, sign in with password and MFA. Open the owner console and use a fresh code from **Hedvi Owner**. The code that confirmed initial enrollment has already been used. Its single-use step opens a separate, session-bound capability for 30 minutes. Five unsuccessful codes within 15 minutes lock new unlocks for 15 minutes. A subsequent successful unlock replaces any previous owner capability. **Lock console**, logout, session revocation and credential changes invalidate elevated access. Losing the authenticator does not create a browser recovery exception.

The operator database role, the web proxy and hosts running the authentication service are trusted infrastructure. The API and worker share the deployment encryption key, but the restricted API database role cannot read or write the independent owner factor or owner sessions. The private worker verifies each owner code. Protect database backups, the operator workstation, the web proxy and worker infrastructure. Never route the private activation envelope through the web service.

## Review accounts and sending

**Find an account** accepts the beginning of an account email or organization name. Enter at least two characters, then select an organization from up to 20 matches. The owner email helps distinguish organizations with the same name. This selects the recipient of the grant and filters existing memberships and sender reviews. **Latest registrations** lists up to 50 accounts with their verification state and sign-up time. Select **Manage** beside an organization to use the same workflow.

**Operational events** shows up to 50 open operations alerts, failed or uncertain account emails, failed webhook deliveries and rejected, retryable or uncertain sends. Each row contains a fixed category, severity, timestamp, event ID and organization ID when available. Use the event ID in a separate, access-controlled incident investigation. The view does not fetch message bodies, recipients, authentication links, webhook responses or raw provider diagnostics.

**Live sending** shows the project's request, website, recipient consent and expected volume. Review this information before approving a project. New senders normally start at 25 recipients per hour and 100 per day. The console accepts hourly limits up to 50,000 and daily limits from the hourly limit to 250,000. Approval requires a prior review request or reviewed project state and an organization with eligible risk status. Pause or adjust a reviewed project here with an audit reason. DNS and SES ownership verification still apply. Decisions record an organization-visible audit entry.

## Complimentary membership accounting

| Gift | Recipients | Attachment data | Term |
|---|---:|---:|---|
| Flex | 10,000 | 250 MiB | 365 days |
| Pro 50k | 50,000 | 1 GiB | 30 days |
| Pro 100k | 100,000 | 2 GiB | 30 days |

A grant starts immediately, has a fixed end and creates no purchase, invoice, renewal or charge. It applies to the organization and leaves live-sending approval and risk limits in force. The customer can see its plan and expiry under Billing even when hosted checkout is disabled.

An organization may have one active gift. Grants are capped at four per organization in 30 days and 30 across the installation in 24 hours, including revoked grants. A grant is refused when a paid Pro period overlaps its proposed term. The form retains its request ID for retries of the same intent. The server returns the original grant for an identical request and rejects reuse of that ID for different details.

A Pro gift uses a distinct usage period. A subsequently confirmed paid Pro cycle supersedes an overlapping Pro gift at its start. Refunding or holding that paid cycle cannot restore the superseded allowance. A Flex gift has its own credit lot. During a later paid Pro cycle, purchased or gifted Flex credits can be used as fallback only under the organization's existing prepaid-credit preference. The existing baseline resumes outside active allowances.

To withdraw a grant, identify the organization and active grant, enter a reason, and select **Revoke remaining allowance**. Revocation prevents further reservations against that gift. A send that already reserved allowance settles against its original source. Grant, revocation and supersession entries retain their source IDs, and the original reservation history stays available for reconciliation. Write reasons that can be shown in the customer's audit log.

## Using the console

- **Overview** shows recent registrations and links to accounts, sending and errors.
- **Accounts** searches by email or organization. Open an account for its Membership, Sending projects and Purchases tabs.
- **Membership** grants a fixed Flex or Pro gift without a charge. The customer-visible audit reason is prefilled; ending a gift removes its unused allowance. Paid purchases remain separate.
- **Sending** shows project status and limits. Normal domain onboarding is automatic. Open Manage only for a limit adjustment or a pause/resume decision.
- **Billing** shows recent purchases and gift history. It does not expose payment credentials.
- **Errors** filters sanitized operational events by severity. Keep event references for incident follow-up.

Ordinary remembered login lasts at most 30 days when explicitly selected at sign-in.
It never extends the separate 30-minute owner unlock. Lock console ends privileged
access while leaving the ordinary account session signed in.
