# Independent public status

The hosted status page at https://status.hedvi.dev is a static GitHub Pages site,
independent of Railway. HTTPS is enforced; the Cloudflare record is DNS-only.
The page obtains current fixed-endpoint observations from the separate Cloudflare
Worker `hedvi-status-probe` when opened, refreshing every minute. The worker caches
results for 60 seconds and has no secrets or storage bindings. Its source is
`apps/status/probe-worker.mjs`; deploy this exact module to the named Worker.
Only GET/HEAD `/status.json` are accepted. Query parameters cannot choose targets,
redirects are rejected, reads are bounded, and failed probes are retried once.
The only allowed browser origin is https://status.hedvi.dev.

The `Hosted status` workflow also publishes a static snapshot and operator incident
notice. A five-minute GitHub schedule is configured, but scheduled execution was
not observed during release acceptance; do not rely on that scheduler for alerts.
Push/manual publishing was verified. The current Cloudflare observations avoid
presenting stale GitHub snapshots as the current state. If Cloudflare is unavailable,
the page falls back only to a sufficiently recent static snapshot. No component has
AWS, Stripe, database or customer credentials. The GitHub scheduled job is disabled
in forks and requires `HEDVI_HOSTED_STATUS=true` in the official hosted repository.

Website and dashboard checks require HTTP 200 HTML. API readiness requires HTTP
200 and the expected database-readiness response. Each failed check is retried.
A failed manually/push-triggered snapshot marks its GitHub workflow as failed.
Cloudflare probes are on demand, not continuous alerting or incident history. Keep the independent
AWS SNS queue, provider and reputation alarms enabled; HTTP checks do not replace
them and do not prove message delivery.

The page shows the observation time and treats missing, incomplete, future-dated
or more-than-20-minute-old results as unknown. GitHub's scheduler may run late;
this is not a five-minute detection guarantee or an uptime SLA.

For a customer-visible incident, update `apps/status/incident.json` with state
`active`, a concise public `title`, a `message` describing impact and the next
update, and `updated_at` in UTC ISO 8601. Never include customer identifiers,
email content, credentials or raw logs. Push the reviewed change to main or run
the workflow manually after publishing it. The notice stays visible even when
HTTP checks pass. Resolve by setting state `none` and clearing the notice fields.
The Git history retains the incident updates; link the final incident review
when appropriate.

Set up GitHub Pages using Actions. Use the `github-pages` deployment environment
restricted to main and allow only Pages deployment and its short-lived OIDC token
in the publishing job. Configure the status subdomain separately from the app;
keep an accessible GitHub Pages URL while waiting for custom-domain TLS.

Check an outage response in staging or by local fixture tests. Do not interrupt
customer production services to test the status page. The status page has no
analytics, cookies or sign-in.
