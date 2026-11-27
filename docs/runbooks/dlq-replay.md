# DLQ replay

Messages carry only ids, and every consumer is idempotent, so replaying is safe.

1. Inspect: `aws sqs receive-message --queue-url <dlq> --max-number-of-messages 10` (do not delete). Check worker logs for the error behind repeated failures and fix it first.
2. Replay: `aws sqs start-message-move-task --source-arn <dlq-arn>` (moves back to the source queue allowed by the redrive policy).
3. Send jobs that never reached SQS are republished by the reaper anyway (stale due emails after 5 minutes); events lost entirely leave recipients at `sent` and should be reconciled from SES if needed.
4. Webhook deliveries that exhausted retries are in state `failed` in PostgreSQL and can be resent from the dashboard with the same event id.
