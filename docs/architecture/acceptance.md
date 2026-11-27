# Durable acceptance and the send pipeline

Status: implemented in `apps/api` and covered by `internal/e2e`. Verified against the local simulator; real AWS behaviour must be verified separately in staging.

```
POST /emails ─► validate (key row decides test/live, domain, size, suppression, loop, policy)
             ─► encrypted payload to S3 (key payloads/<org>/<email>)
             ─► ONE PostgreSQL transaction:
                   emails + recipients + rate windows + quota reservation
                   + outbox job + idempotency record (+ suppressed webhooks)
             ─► 200 {"id"} only after commit
outbox dispatcher ─► SQS send queue (at least once)
send worker ─► conditional claim with lease ─► re-check pause/suppression
            ─► commit send_attempt "started" ─► SES SendEmail (raw MIME, no SDK retries)
            ─► accepted | rejected | retryable | unknown
SES ─► SNS ─► SQS events ─► idempotent event worker ─► per-recipient status, suppression,
                                                       quota settlement, webhook outbox
webhook worker ─► signed POST through SSRF-safe client, retry schedule, failed state = DLQ
```

## Invariants and where they are enforced

| Invariant | Enforcement | Test |
|---|---|---|
| No `200` without durable acceptance | single transaction in `accept.Service.Send` | `TestCrashBeforeCommitAcceptsNothing` |
| Crash after upload leaves no accepted mail | payload uploaded before tx; orphan sweeper (age ≥ 1 h) | `TestCrashAfterPayloadUploadLeavesOnlyAnOrphan` |
| Lost response is recoverable | idempotency record in same tx, scoped org/project/operation/test-or-live-mode, payload hash | `TestCrashAfterCommitIsRecoveredByIdempotencyKey`, `TestIdempotencyReplayConflictAndConcurrency` |
| Parallel requests cannot exceed quota | period row `FOR UPDATE`, CHECK constraints | `TestParallelReservationsNeverExceedQuota` |
| Test keys never reach SES | mode from key row; DB triggers refuse send outbox and attempts for test mail | `TestTestKeysNeverReachTheProvider` |
| Duplicate queue messages send once | conditional claim on `send_state` | `TestDuplicatePublishAfterDispatcherCrashSendsOnce` |
| Ambiguous provider outcome is never blindly retried | attempt row before call; reaper marks `unknown`; SES events reconcile | `TestWorkerCrashAfterSESAcceptedIsReconciledWithoutResend`, `TestCrashBeforeProviderCallIsUnknownNotRetried` |
| Pause stops queued jobs | worker re-checks project/org before each attempt; `held` state | `TestPauseHoldsQueuedJobsUntilApproved` |
| Late/duplicate events never overwrite | event dedupe key + status rank | `TestEventsAreIdempotentAndOrderIndependent` |
| Tenants are isolated in the database | RLS for the API role, explicit worker policies, security-definer lookups | `TestRowLevelSecurityIsolatesTenants`, `TestConsoleSignupIsolationAndCSRF` |

## Payload and database are not one transaction

S3 and PostgreSQL cannot commit atomically. The payload is written first; a failed commit leaves an object without a row. Workers only send emails that have a row, so an orphan is never sent. The sweeper deletes objects older than one hour without a referencing row; the bucket lifecycle rule is the last backstop.

## Queue semantics

SQS Standard can deliver twice and the dispatcher can publish twice (crash between publish and commit). Consumers are idempotent: the send claim is `UPDATE … WHERE send_state IN ('queued','retry_wait')`. If a message is lost or dead-lettered, the reaper republishes due emails that made no progress for 5 minutes. PostgreSQL is the source of truth; Valkey is only used for rate limiting.
