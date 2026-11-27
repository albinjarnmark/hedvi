# Operator-only account email. AWS provider 6.73.0 schema has no tenant-level
# suppression_scope field; this config deliberately enables ACCOUNT-list
# BOUNCE/COMPLAINT suppression. The customer config opts out of both reasons.
locals {
  auth_enabled               = var.auth_mail_domain != ""
  auth_name                  = "operator-hedvi-${var.env}-auth"
  auth_from                  = var.auth_mail_from != "" ? var.auth_mail_from : "accounts@${var.auth_mail_domain}"
  auth_identity_arn          = "${local.ses_arn}:identity/${var.auth_mail_domain}"
  auth_configuration_set_arn = "${local.ses_arn}:configuration-set/${local.auth_name}"
  auth_domain_labels         = split(".", var.auth_mail_domain)
  # Block namespace mutations on ancestors as well as domain/email identities
  # below the auth domain. Application checks reserve the same DNS namespace.
  auth_ancestor_identity_arns = local.auth_enabled ? [for i in range(length(local.auth_domain_labels) - 1) : "${local.ses_arn}:identity/${join(".", slice(local.auth_domain_labels, i, length(local.auth_domain_labels)))}"] : []
  auth_guard_resources = concat(local.auth_ancestor_identity_arns, [
    "${local.ses_arn}:identity/*.${var.auth_mail_domain}",
    "${local.ses_arn}:identity/*@${var.auth_mail_domain}",
    "${local.ses_arn}:identity/*@*.${var.auth_mail_domain}",
    "${local.ses_arn}:tenant/${local.auth_name}",
    "${local.ses_arn}:tenant/${local.auth_name}/*",
    local.auth_configuration_set_arn,
  ])
}
resource "aws_sesv2_configuration_set" "auth" {
  count                  = local.auth_enabled ? 1 : 0
  configuration_set_name = local.auth_name
  delivery_options { tls_policy = "REQUIRE" }
  reputation_options { reputation_metrics_enabled = true }
  sending_options { sending_enabled = true }
  suppression_options { suppressed_reasons = ["BOUNCE", "COMPLAINT"] }
  tags = merge(local.tags, { purpose = "operator-auth" })
}
resource "aws_sesv2_email_identity" "auth" {
  count          = local.auth_enabled ? 1 : 0
  email_identity = var.auth_mail_domain
  # SES uses this when SendEmail omits ConfigurationSetName. IAM separately
  # rejects explicitly requesting any other set with the operator tenant.
  configuration_set_name = aws_sesv2_configuration_set.auth[0].configuration_set_name
  dkim_signing_attributes { next_signing_key_length = "RSA_2048_BIT" }
  tags = merge(local.tags, { purpose = "operator-auth" })
}
resource "aws_sesv2_email_identity_mail_from_attributes" "auth" {
  count                  = local.auth_enabled ? 1 : 0
  email_identity         = aws_sesv2_email_identity.auth[0].email_identity
  mail_from_domain       = "bounce.${var.auth_mail_domain}"
  behavior_on_mx_failure = "REJECT_MESSAGE"
}
resource "aws_sesv2_tenant" "auth" {
  count       = local.auth_enabled ? 1 : 0
  tenant_name = local.auth_name
  tags        = merge(local.tags, { purpose = "operator-auth" })
}
resource "aws_sesv2_tenant_resource_association" "auth_identity" {
  count        = local.auth_enabled ? 1 : 0
  tenant_name  = aws_sesv2_tenant.auth[0].tenant_name
  resource_arn = aws_sesv2_email_identity.auth[0].arn
}
resource "aws_sesv2_tenant_resource_association" "auth_configuration_set" {
  count        = local.auth_enabled ? 1 : 0
  tenant_name  = aws_sesv2_tenant.auth[0].tenant_name
  resource_arn = aws_sesv2_configuration_set.auth[0].arn
}
resource "aws_sesv2_configuration_set_event_destination" "auth" {
  count                  = local.auth_enabled ? 1 : 0
  configuration_set_name = aws_sesv2_configuration_set.auth[0].configuration_set_name
  event_destination_name = "hedvi-auth-events"
  event_destination {
    enabled              = true
    matching_event_types = ["SEND", "REJECT", "BOUNCE", "COMPLAINT", "DELIVERY", "DELIVERY_DELAY", "RENDERING_FAILURE"]
    sns_destination { topic_arn = aws_sns_topic.ses_events.arn }
  }
  depends_on = [aws_sns_topic_policy.ses_events]
}
# Shared explicit deny is included in BOTH service policies, even if future
# allow statements widen. Read/send are evaluated against separate grants.
data "aws_iam_policy_document" "auth_guard" {
  dynamic "statement" {
    for_each = local.auth_enabled ? [1] : []
    content {
      sid         = "DenyOperatorAuthNamespaceMutation"
      effect      = "Deny"
      not_actions = ["ses:GetEmailIdentity", "ses:GetTenant", "ses:GetConfigurationSet", "ses:SendEmail", "ses:SendRawEmail"]
      resources   = local.auth_guard_resources
    }
  }
  dynamic "statement" {
    for_each = local.auth_enabled ? [1] : []
    content {
      sid       = "DenyAuthMailboxOutsideOperatorTenant"
      effect    = "Deny"
      actions   = ["ses:SendEmail", "ses:SendRawEmail"]
      resources = ["*"]
      condition {
        test     = "StringEqualsIgnoreCase"
        variable = "ses:FromAddress"
        values   = [local.auth_from]
      }
      condition {
        test     = "StringNotEquals"
        variable = "ses:TenantName"
        values   = [local.auth_name]
      }
    }
  }
}
