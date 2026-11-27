output "env" {
  description = "Environment variables for the Hedvi processes (non-secret)."
  value = merge({
    HEDVI_AWS_MODE                = "aws"
    HEDVI_AWS_REGION              = data.aws_region.current.region
    HEDVI_AWS_ACCOUNT_ID          = local.account_id
    HEDVI_S3_BUCKET               = aws_s3_bucket.payloads.bucket
    HEDVI_S3_KMS_KEY_ID           = aws_kms_key.data.arn
    HEDVI_SQS_SEND_QUEUE_URL      = aws_sqs_queue.main["send"].url
    HEDVI_SQS_EVENTS_QUEUE_URL    = aws_sqs_queue.main["events"].url
    HEDVI_SQS_WEBHOOK_QUEUE_URL   = aws_sqs_queue.main["webhook"].url
    HEDVI_SES_CONFIGURATION_SET   = aws_sesv2_configuration_set.main.configuration_set_name
    HEDVI_SES_TENANT_PREFIX       = local.customer_tenant_prefix
    HEDVI_SES_MAIL_FROM_MX        = "feedback-smtp.${data.aws_region.current.region}.amazonses.com"
    HEDVI_MAX_QUEUE_TIME          = "${var.max_queue_days * 24}h"
    HEDVI_OPS_ALERT_SNS_TOPIC_ARN = aws_sns_topic.ops.arn
    }, local.auth_enabled ? {
    HEDVI_AUTH_MAIL_FROM              = local.auth_from
    HEDVI_AUTH_MAIL_TENANT            = local.auth_name
    HEDVI_AUTH_MAIL_CONFIGURATION_SET = local.auth_name
  } : {})
}

output "api_policy_arn" {
  value = aws_iam_policy.api.arn
}

output "worker_policy_arn" {
  value = aws_iam_policy.worker.arn
}

output "ops_topic_arn" {
  value = aws_sns_topic.ops.arn
}

output "auth_mail_dns_records" {
  description = "Publish these full DNS names without proxying. Token records are populated after operator apply; no Cloudflare credentials are needed by this module."
  value = local.auth_enabled ? concat([
    for token in aws_sesv2_email_identity.auth[0].dkim_signing_attributes[0].tokens : {
      type = "CNAME", name = "${token}._domainkey.${var.auth_mail_domain}", value = "${token}.dkim.amazonses.com", priority = null, ttl = 300
    }
    ], [
    { type = "MX", name = "bounce.${var.auth_mail_domain}", value = "feedback-smtp.${data.aws_region.current.region}.amazonses.com", priority = 10, ttl = 300 },
    { type = "TXT", name = "bounce.${var.auth_mail_domain}", value = "v=spf1 include:amazonses.com -all", priority = null, ttl = 300 },
    { type = "TXT", name = "_dmarc.${var.auth_mail_domain}", value = "v=DMARC1; p=reject; adkim=s; aspf=r", priority = null, ttl = 300 },
  ]) : []
}
output "auth_mail_resources" {
  description = "Operator-owned resources; API and worker policies deny their mutation."
  value = local.auth_enabled ? {
    identity_arn          = aws_sesv2_email_identity.auth[0].arn
    tenant_arn            = aws_sesv2_tenant.auth[0].tenant_arn
    configuration_set_arn = aws_sesv2_configuration_set.auth[0].arn
    suppression_scope     = "ACCOUNT"
  } : null
}
