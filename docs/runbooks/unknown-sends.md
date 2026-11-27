# Resolving delivery_unknown

1. Staff page → Unknown send outcomes (or `SELECT … FROM send_attempts WHERE state = 'unknown'`).
2. In SES (CloudWatch Logs / event destination archive) search for the tag `hedvi_attempt=<attempt id>`. A Send/Delivery event means SES accepted it — normally reconciled automatically.
3. Resolve: `hedvi admin resolve-unknown -attempt <id> -outcome accepted|not_sent -note "<evidence>"`. `not_sent` releases quota and marks recipients failed; neither resends.
4. If the customer wants the email sent after `not_sent`, they use "Resend as new email" (new id, quota, audit).
