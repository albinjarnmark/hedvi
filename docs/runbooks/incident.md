# Incident response (pilot)

1. **Detect:** CloudWatch alarm or Hedvi alert (ops SNS email / `HEDVI_OPS_ALERT_WEBHOOK_URL`). Owner on call: named in the pilot roster.
2. **Assess (≤ 15 min):** Is acceptance failing (API 5xx, `/readyz`)? Are jobs stuck (`hedvi_oldest_queued_email_seconds`, DLQ depth)? Are outcomes unknown (`hedvi_unknown_send_attempts`)? Is reputation at risk (SES bounce/complaint alarms)?
3. **Contain:** pause affected projects (`hedvi admin pause-project`) — queued mail is held, not lost. For provider trouble do nothing destructive; retryable errors back off automatically.
4. **Communicate:** update the independent [status page](status-page.md) within 30 minutes of a customer-visible incident; contact affected customers directly. This is an internal response target, not a contractual promise.
5. **Recover:** replay DLQ (`dlq-replay.md`), resolve unknown attempts (`unknown-sends.md`), resume projects.
6. **Review:** written post-incident note within 5 business days with timeline, impact (emails, recipients), root cause and actions.
