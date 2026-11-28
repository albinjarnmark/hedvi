# Sending domains

## Proving ownership and obtaining sending records

1. Create a domain in the project. Hedvi returns a pending domain and its unique TXT record at `_hedvi-verification.<sending-domain>`. The value is `hedvi-domain=<claim UUID>`. Several projects can request challenges for the same name. Each challenge expires 72 hours after creation.
2. Publish the TXT value and use **Check now** or `POST /domains/{id}/verify`. Hedvi looks up the absolute DNS name and matches the entire TXT value. An organization owner or admin must have accepted the current terms. A restricted account remains closed and receives a support instruction. No manual application is needed.
3. With current DNS proof and accepted terms, Hedvi reserves the canonical name, provisions the tagged SES identity and returns the Easy DKIM CNAMEs and custom MAIL FROM MX/TXT records. The MAIL FROM subdomain defaults to `send.<domain>`. Existing root SPF and DMARC policies are displayed in the DNS guide.
4. Publish the sending records and keep the ownership TXT in DNS. The domain becomes `verified` once the current challenge, required sending records and provider verification pass. A replacement claim requires a new TXT value even when SES reuses DKIM selectors.

After provisioning, DNS checks back off from 30 seconds to six hours. An unverified domain stops polling after 72 hours. **Check now** restarts the verification window. Verified domains are checked daily. The local simulator can supply records for `.test` names. Deployed environments reject `.test`, `.example`, `.localhost`, `.invalid` and `.local` sending domains.

## Provider custody and migration

SES stores the application domain UUID in the `hedvi:domain-claim` identity tag. A global unique index protects the reservation from the moment the eligible project proves DNS ownership until provider deletion succeeds. A timed-out operation retains the reservation. Retry verification or deletion on the same domain ID. If the original owner is unavailable or SES has a conflicting tag, an operator must confirm ownership independently, reconcile the old identity in the correct account and region, and complete deletion before another project provisions the name.

DNS and read-only SES checks run outside database transactions. Conditional writes discard stale results if the domain changed during a check. Provider create/delete operations use PostgreSQL session advisory locks with at most two pinned connections per process. Connect the API directly to PostgreSQL or through a session-mode pooler so each pgx connection retains its database session.

Drain old API and worker processes before upgrading. Migration `0005_domain_dns_ownership` resets historical verification and requires each active owner to publish the current TXT. Migration `0008_domain_challenges` retains existing provider reservations, including interrupted operations, and changes uniqueness for newly created DNS challenges. Reconcile questionable existing reservations individually. Apply the migrations before restarting sending and domain verification.

## Automatic first activation

A verified organization owner or administrator must have an exact receipt for the
current adopted document manifest. The API may provision a fresh domain only after
its claim-specific ownership TXT is present and the organization/project is not
restricted. It cannot approve projects or raise their sending limits.

The worker independently verifies ownership TXT, DKIM, MAIL FROM and provider
identity before activating a new project. It requires the environment-bound tenant,
rechecks consent and risk under organization/project locks, and records one
`project.live_auto_activated` audit event. Paused sibling projects prevent new
automatic activation. Existing approved limits and observation windows remain
unchanged. Startup limits are 25 recipients/hour and 100/day per project.

Domain claim identifiers and ownership coordinates cannot be rewritten or physically
deleted by the API role. Provider verification commits only if the checked resource
snapshot still matches. Provider-pending checks follow bounded propagation backoff.
