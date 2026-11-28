# Hedvi data plane in eu-north-1: payload storage, queues, SES events, IAM
# and alarms. Compute (API/workers), PostgreSQL and Valkey are deliberately
# separate decisions; see infra/terraform/README.md.
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = { source = "hashicorp/aws", version = "6.73.0" }
  }
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  prefix                = "${var.name}-${var.env}"
  account_id            = data.aws_caller_identity.current.account_id
  ses_arn               = "arn:aws:ses:${data.aws_region.current.region}:${local.account_id}"
  identity_arn          = "${local.ses_arn}:identity/*"
  tenant_arn            = "${local.ses_arn}:tenant/${local.customer_tenant_prefix}*"
  configuration_set_arn = "${local.ses_arn}:configuration-set/${local.prefix}"
  alarm_arn             = "arn:aws:cloudwatch:${data.aws_region.current.region}:${local.account_id}:alarm:${local.prefix}-*"
  ownership_tag         = "hedvi:domain-claim"
  claim_pattern         = "????????-????-????-????-????????????"
  # Payload objects outlive content retention only by the queue time; the
  # lifecycle rule is a backstop behind the application's own deletion.
  payload_backstop_days = var.content_max_days + var.max_queue_days + 1
  tags = {
    project     = var.name
    environment = var.env
    managed_by  = "terraform"
  }
}

# ---------------------------------------------------------------------------
# Encryption
# ---------------------------------------------------------------------------

resource "aws_kms_key" "data" {
  description             = "${local.prefix} payloads, queues and topics"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  tags                    = local.tags
  policy                  = data.aws_iam_policy_document.kms.json
}

data "aws_iam_policy_document" "kms" {
  statement {
    sid       = "AccountAdmin"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${local.account_id}:root"]
    }
  }
  statement {
    sid       = "SESAndSNSPublishEncrypted"
    actions   = ["kms:GenerateDataKey*", "kms:Decrypt"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["ses.amazonaws.com", "sns.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
  }
  statement {
    sid       = "CloudWatchAlarmNotifications"
    actions   = ["kms:GenerateDataKey*", "kms:Decrypt"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["cloudwatch.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = [local.alarm_arn]
    }
  }
}

resource "aws_kms_alias" "data" {
  name          = "alias/${local.prefix}-data"
  target_key_id = aws_kms_key.data.key_id
}

# ---------------------------------------------------------------------------
# Payload bucket: private, SSE-KMS, no versions kept, lifecycle backstop.
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "payloads" {
  bucket = "${local.prefix}-payloads-${local.account_id}"
  tags   = local.tags
}

resource "aws_s3_bucket_public_access_block" "payloads" {
  bucket                  = aws_s3_bucket.payloads.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "payloads" {
  bucket = aws_s3_bucket.payloads.id
  rule { object_ownership = "BucketOwnerEnforced" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "payloads" {
  bucket = aws_s3_bucket.payloads.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.data.arn
    }
    bucket_key_enabled = true
  }
}

# Versioning stays disabled so deleted content does not survive as old versions.
resource "aws_s3_bucket_versioning" "payloads" {
  bucket = aws_s3_bucket.payloads.id
  versioning_configuration { status = "Disabled" }
}

resource "aws_s3_bucket_lifecycle_configuration" "payloads" {
  bucket = aws_s3_bucket.payloads.id
  rule {
    id     = "payload-backstop"
    status = "Enabled"
    filter { prefix = "payloads/" }
    expiration { days = local.payload_backstop_days }
    abort_incomplete_multipart_upload { days_after_initiation = 1 }
  }
}

data "aws_iam_policy_document" "payloads" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    resources = [aws_s3_bucket.payloads.arn, "${aws_s3_bucket.payloads.arn}/*"]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
  statement {
    sid     = "DenyUnencryptedPuts"
    effect  = "Deny"
    actions = ["s3:PutObject"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    resources = ["${aws_s3_bucket.payloads.arn}/*"]
    condition {
      test     = "StringNotEquals"
      variable = "s3:x-amz-server-side-encryption"
      values   = ["aws:kms"]
    }
  }
}

resource "aws_s3_bucket_policy" "payloads" {
  bucket = aws_s3_bucket.payloads.id
  policy = data.aws_iam_policy_document.payloads.json
}

# ---------------------------------------------------------------------------
# Queues with dead-letter queues
# ---------------------------------------------------------------------------

locals {
  queues = {
    send    = { visibility = 180, max_receive = 5 }
    events  = { visibility = 60, max_receive = 8 }
    webhook = { visibility = 60, max_receive = 5 }
  }
}

resource "aws_sqs_queue" "dlq" {
  for_each                  = local.queues
  name                      = "${local.prefix}-${each.key}-dlq"
  message_retention_seconds = 1209600
  kms_master_key_id         = aws_kms_key.data.id
  tags                      = local.tags
}

resource "aws_sqs_queue" "main" {
  for_each                   = local.queues
  name                       = "${local.prefix}-${each.key}"
  visibility_timeout_seconds = each.value.visibility
  receive_wait_time_seconds  = 10
  message_retention_seconds  = 345600
  kms_master_key_id          = aws_kms_key.data.id
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq[each.key].arn
    maxReceiveCount     = each.value.max_receive
  })
  tags = local.tags
}

resource "aws_sqs_queue_redrive_allow_policy" "dlq" {
  for_each  = local.queues
  queue_url = aws_sqs_queue.dlq[each.key].id
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.main[each.key].arn]
  })
}

# ---------------------------------------------------------------------------
# SES events: configuration set -> SNS -> SQS (raw delivery)
# ---------------------------------------------------------------------------

resource "aws_sns_topic" "ses_events" {
  name              = "${local.prefix}-ses-events"
  kms_master_key_id = aws_kms_key.data.id
  tags              = local.tags
}

# Only SES in this account, for this configuration set, may publish.
data "aws_iam_policy_document" "ses_events_topic" {
  statement {
    sid       = "AllowSESConfigurationSet"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.ses_events.arn]
    principals {
      type        = "Service"
      identifiers = ["ses.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = concat([local.configuration_set_arn], local.auth_enabled ? [local.auth_configuration_set_arn] : [])
    }
  }
}

resource "aws_sns_topic_policy" "ses_events" {
  arn    = aws_sns_topic.ses_events.arn
  policy = data.aws_iam_policy_document.ses_events_topic.json
}

data "aws_iam_policy_document" "events_queue" {
  statement {
    sid       = "AllowSESEventsTopic"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.main["events"].arn]
    principals {
      type        = "Service"
      identifiers = ["sns.amazonaws.com"]
    }
    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_sns_topic.ses_events.arn]
    }
  }
}

resource "aws_sqs_queue_policy" "events" {
  queue_url = aws_sqs_queue.main["events"].id
  policy    = data.aws_iam_policy_document.events_queue.json
}

resource "aws_sns_topic_subscription" "events_to_sqs" {
  topic_arn            = aws_sns_topic.ses_events.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.main["events"].arn
  raw_message_delivery = true
}

resource "aws_sesv2_configuration_set" "main" {
  configuration_set_name = local.prefix
  delivery_options {
    tls_policy = "OPTIONAL"
  }
  reputation_options {
    reputation_metrics_enabled = true
  }
  sending_options {
    sending_enabled = true
  }
  # Account-level suppression would let one customer's bounce block another
  # customer's recipients. Hedvi keeps suppression per organization instead.
  suppression_options {
    suppressed_reasons = []
  }
  tags = local.tags
}

resource "aws_sesv2_configuration_set_event_destination" "sns" {
  configuration_set_name = aws_sesv2_configuration_set.main.configuration_set_name
  event_destination_name = "hedvi-events"
  event_destination {
    enabled              = true
    matching_event_types = ["SEND", "REJECT", "BOUNCE", "COMPLAINT", "DELIVERY", "DELIVERY_DELAY", "RENDERING_FAILURE"]
    sns_destination {
      topic_arn = aws_sns_topic.ses_events.arn
    }
  }
  depends_on = [aws_sns_topic_policy.ses_events]
}

# ---------------------------------------------------------------------------
# IAM: least privilege per role. The API never sends; workers never manage
# identities. Customers never receive AWS credentials.
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "api" {
  source_policy_documents = [data.aws_iam_policy_document.auth_guard.json, data.aws_iam_policy_document.environment_guard.json]
  statement {
    sid       = "DenyAPISending"
    effect    = "Deny"
    actions   = ["ses:Send*"]
    resources = ["*"]
  }
  statement {
    sid       = "PayloadWrite"
    actions   = ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"]
    resources = ["${aws_s3_bucket.payloads.arn}/payloads/*"]
  }
  statement {
    sid       = "KMS"
    actions   = ["kms:GenerateDataKey", "kms:Decrypt"]
    resources = [aws_kms_key.data.arn]
  }
  statement {
    sid       = "ReadIdentityForClaimCheck"
    actions   = ["ses:GetEmailIdentity"]
    resources = [local.identity_arn]
  }
  # SES authorizes TagResource when CreateEmailIdentity includes tags.
  # No grant to remove tags or replace an existing claim with a new value.
  statement {
    sid       = "CreateIdentityWithImmutableClaim"
    actions   = ["ses:CreateEmailIdentity", "ses:TagResource"]
    resources = [local.identity_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/environment"
      values   = [var.env]
    }
    condition {
      test     = "StringEqualsIfExists"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
    condition {
      test     = "StringLike"
      variable = "aws:RequestTag/${local.ownership_tag}"
      values   = [local.claim_pattern]
    }
    condition {
      test     = "ForAllValues:StringEquals"
      variable = "aws:TagKeys"
      values   = [local.ownership_tag, "environment"]
    }
    condition {
      test     = "StringEqualsIfExists"
      variable = "aws:ResourceTag/${local.ownership_tag}"
      values   = ["&{aws:RequestTag/${local.ownership_tag}}"]
    }
  }
  statement {
    sid       = "MutateClaimedIdentity"
    actions   = ["ses:DeleteEmailIdentity", "ses:PutEmailIdentityMailFromAttributes", "ses:CreateTenantResourceAssociation"]
    resources = [local.identity_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
    condition {
      test     = "StringLike"
      variable = "aws:ResourceTag/${local.ownership_tag}"
      values   = [local.claim_pattern]
    }
  }
  statement {
    sid       = "CreateProjectTenant"
    actions   = ["ses:CreateTenant", "ses:TagResource"]
    resources = [local.tenant_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/environment"
      values   = [var.env]
    }
    condition {
      test     = "StringEqualsIfExists"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
    condition {
      test     = "ForAllValues:StringEquals"
      variable = "aws:TagKeys"
      values   = ["environment"]
    }
  }
  statement {
    sid       = "CreateProjectTenantRead"
    actions   = ["ses:GetTenant"]
    resources = [local.tenant_arn]
  }
  statement {
    sid       = "CreateProjectTenantAssociate"
    actions   = ["ses:CreateTenantResourceAssociation"]
    resources = [local.tenant_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
  }
  statement {
    sid       = "AssociateHedviConfigurationSet"
    actions   = ["ses:CreateTenantResourceAssociation"]
    resources = [local.configuration_set_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
  }
}

data "aws_iam_policy_document" "worker" {
  source_policy_documents = [data.aws_iam_policy_document.auth_guard.json, data.aws_iam_policy_document.environment_guard.json]
  statement {
    sid       = "OperationalAlerts"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.ops.arn]
  }
  statement {
    sid       = "PayloadReadDelete"
    actions   = ["s3:GetObject", "s3:DeleteObject"]
    resources = ["${aws_s3_bucket.payloads.arn}/payloads/*"]
  }
  statement {
    sid       = "PayloadList"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.payloads.arn]
  }
  statement {
    sid       = "KMS"
    actions   = ["kms:GenerateDataKey", "kms:Decrypt"]
    resources = [aws_kms_key.data.arn]
  }
  statement {
    sid = "Queues"
    actions = ["sqs:SendMessage", "sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility",
    "sqs:GetQueueAttributes"]
    resources = [for q in aws_sqs_queue.main : q.arn]
  }
  statement {
    # Send authorization is evaluated against the identity and configuration
    # set ARNs, so a deny on the foreign tenant ARN alone cannot protect it.
    sid       = "DenyForeignSendTenant"
    effect    = "Deny"
    actions   = ["ses:SendEmail", "ses:SendRawEmail"]
    resources = ["*"]
    condition {
      test     = "StringLike"
      variable = "ses:TenantName"
      values   = [local.foreign_customer_send_pattern, "operator-hedvi-${local.foreign_environment}-auth"]
    }
  }
  statement {
    # The worker uses SES v2 SendEmail with Content.Raw. Its IAM action is
    # SendRawEmail. ResourceTag/environment is not reliably supplied for RAW.
    # SES verifies the identity and configuration set belong to the requested
    # tenant. Their association still requires strict environment tags.
    sid       = "SendCustomerRaw"
    actions   = ["ses:SendRawEmail"]
    resources = [local.identity_arn, local.configuration_set_arn]
    condition {
      test     = "StringLike"
      variable = "ses:TenantName"
      values   = [local.customer_send_tenant_pattern]
    }
    dynamic "condition" {
      for_each = local.auth_enabled ? [1] : []
      content {
        # Case insensitive comparison closes parent-domain authorization and
        # differently cased mailbox variants as routes to the auth sender.
        test     = "StringNotEqualsIgnoreCase"
        variable = "ses:FromAddress"
        values   = [local.auth_from]
      }
    }
    dynamic "condition" {
      for_each = local.auth_enabled ? [1] : []
      content {
        test     = "StringNotLike"
        variable = "ses:FromAddress"
        values   = ["*@${var.auth_mail_domain}", "*@*.${var.auth_mail_domain}"]
      }
    }
  }
  dynamic "statement" {
    for_each = local.auth_enabled ? [1] : []
    content {
      sid       = "SendOperatorAuthMail"
      actions   = ["ses:SendEmail", "ses:SendRawEmail"]
      resources = [local.auth_identity_arn, local.auth_configuration_set_arn]
      condition {
        test     = "StringEquals"
        variable = "ses:FromAddress"
        values   = [local.auth_from]
      }
      condition {
        test     = "StringEquals"
        variable = "ses:TenantName"
        values   = [local.auth_name]
      }
    }
  }
  statement {
    sid       = "IdentityStatus"
    actions   = ["ses:GetEmailIdentity"]
    resources = [local.identity_arn]
  }
  # The operator approve-project command runs with the worker task role.
  statement {
    sid       = "ApproveProjectTenant"
    actions   = ["ses:CreateTenant", "ses:TagResource"]
    resources = [local.tenant_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/environment"
      values   = [var.env]
    }
    condition {
      test     = "StringEqualsIfExists"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
    condition {
      test     = "ForAllValues:StringEquals"
      variable = "aws:TagKeys"
      values   = ["environment"]
    }
  }
  statement {
    sid       = "ApproveProjectTenantRead"
    actions   = ["ses:GetTenant"]
    resources = [local.tenant_arn]
  }
  statement {
    sid       = "ApproveProjectTenantAssociate"
    actions   = ["ses:CreateTenantResourceAssociation"]
    resources = [local.tenant_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
  }
  statement {
    sid       = "AssociateApprovalConfigurationSet"
    actions   = ["ses:CreateTenantResourceAssociation"]
    resources = [local.configuration_set_arn]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/environment"
      values   = [var.env]
    }
  }
}

resource "aws_iam_policy" "api" {
  name   = "${local.prefix}-api"
  policy = data.aws_iam_policy_document.api.json
}

resource "aws_iam_policy" "worker" {
  name   = "${local.prefix}-worker"
  policy = data.aws_iam_policy_document.worker.json
}

# ---------------------------------------------------------------------------
# Alarms on an independent channel (SNS email), not through Hedvi itself.
# ---------------------------------------------------------------------------

resource "aws_sns_topic" "ops" {
  name              = "${local.prefix}-ops-alarms"
  kms_master_key_id = aws_kms_key.data.arn
  tags              = local.tags
}

data "aws_iam_policy_document" "ops_topic" {
  statement {
    sid       = "AllowAccountCloudWatchAlarms"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.ops.arn]
    principals {
      type        = "Service"
      identifiers = ["cloudwatch.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = [local.alarm_arn]
    }
  }
}
resource "aws_sns_topic_policy" "ops" {
  arn    = aws_sns_topic.ops.arn
  policy = data.aws_iam_policy_document.ops_topic.json
}

resource "aws_sns_topic_subscription" "ops_email" {
  for_each  = toset(var.ops_alert_emails)
  topic_arn = aws_sns_topic.ops.arn
  protocol  = "email"
  endpoint  = each.value
}

resource "aws_cloudwatch_metric_alarm" "dlq_not_empty" {
  for_each            = local.queues
  alarm_name          = "${local.prefix}-${each.key}-dlq-not-empty"
  alarm_description   = "Messages in the ${each.key} DLQ. See docs/runbooks/dlq-replay.md."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = aws_sqs_queue.dlq[each.key].name }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.ops.arn]
  ok_actions          = [aws_sns_topic.ops.arn]
}

resource "aws_cloudwatch_metric_alarm" "queue_age" {
  for_each            = local.queues
  alarm_name          = "${local.prefix}-${each.key}-oldest-message"
  alarm_description   = "Oldest ${each.key} message older than 5 minutes."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateAgeOfOldestMessage"
  dimensions          = { QueueName = aws_sqs_queue.main[each.key].name }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 5
  threshold           = 300
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.ops.arn]
  ok_actions          = [aws_sns_topic.ops.arn]
}

# Account reputation: SES pauses accounts above roughly 10 % bounces and
# 0.5 % complaints; alarm well before that.
resource "aws_cloudwatch_metric_alarm" "ses_bounce_rate" {
  alarm_name          = "${local.prefix}-ses-bounce-rate"
  alarm_description   = "SES account bounce rate above 4 %."
  namespace           = "AWS/SES"
  metric_name         = "Reputation.BounceRate"
  statistic           = "Maximum"
  period              = 900
  evaluation_periods  = 1
  threshold           = 0.04
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.ops.arn]
}

resource "aws_cloudwatch_metric_alarm" "ses_complaint_rate" {
  alarm_name          = "${local.prefix}-ses-complaint-rate"
  alarm_description   = "SES account complaint rate above 0.08 %."
  namespace           = "AWS/SES"
  metric_name         = "Reputation.ComplaintRate"
  statistic           = "Maximum"
  period              = 900
  evaluation_periods  = 1
  threshold           = 0.0008
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.ops.arn]
}

resource "aws_budgets_budget" "monthly" {
  count        = length(var.budget_alert_emails) > 0 ? 1 : 0
  name         = "${local.prefix}-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"
  # Show gross usage even while startup credits pay the invoice.
  cost_types {
    include_credit = false
    include_refund = false
  }
  dynamic "notification" {
    for_each = [50, 80, 100]
    content {
      comparison_operator        = "GREATER_THAN"
      threshold                  = notification.value
      threshold_type             = "PERCENTAGE"
      notification_type          = "ACTUAL"
      subscriber_email_addresses = var.budget_alert_emails
    }
  }
}
