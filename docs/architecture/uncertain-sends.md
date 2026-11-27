# Policy for uncertain send attempts

SES `SendEmail` is not transactional with Hedvi's database. If SES accepts a message and the worker crashes before recording it, a new attempt would create a duplicate. Neither SQS FIFO nor API idempotency solves this. Hedvi therefore never promises "no duplicates"; it promises that it never creates one **automatically**.

1. **Record before calling.** A `send_attempts` row in state `started` is committed before the SES call. Tags `hedvi_email`, `hedvi_attempt` and `hedvi_project` (no personal data) go with the message.
2. **No hidden retries.** The SES client uses `aws.NopRetryer`; Hedvi classifies every outcome (`internal/provider/ses.go`):
   - *Accepted*: message id returned → recipients `sent`, quota consumed.
   - *Rejected*: 4xx such as `MessageRejected` → `failed`, quota released.
   - *Retryable*: throttling, `503`, connection refused/DNS before sending, `SendingPaused` (with alert) → backoff, same email.
   - *Unknown*: timeouts, resets after the request was written, `500 InternalFailure` → `delivery_unknown`.
3. **Unknown stays reserved.** Recipients show `delivery_unknown`, quota stays reserved, an alert opens and an opt-in webhook `hedvi.email.delivery_unknown` is sent.
4. **Reconcile from events.** Any SES event carrying the attempt tag (Send, Delivery, Bounce …) settles the attempt as `resolved_accepted` and consumes quota.
5. **Escalate.** After 30 minutes without an event the alert becomes critical. Staff investigate in SES (search by tag) and resolve as `accepted` or `not_sent` (`hedvi admin resolve-unknown` or the staff page). Resolving never resends.
6. **Deliberate resend is a new email.** "Resend" in the dashboard creates a new email with `resend_of`, new quota and an audit entry, after warning about duplicates.
7. **Lost workers.** If a lease expires and a `started` attempt exists, the reaper marks it unknown; if no attempt exists, the provider was never called and the job is safely re-queued.
