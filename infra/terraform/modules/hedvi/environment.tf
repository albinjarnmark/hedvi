# SES tenant names and identity tags jointly separate customer environments.
# Prefixes alone cannot isolate SES identity/*, which has no tenant in its ARN.
locals {
  customer_tenant_prefix = var.ses_tenant_prefix != "" ? var.ses_tenant_prefix : (var.env == "production" ? "hedvi-production-" : "hedvi-")
  foreign_environment    = var.env == "production" ? "staging" : "production"
  # IAM '?' matches one character. The application derives the 32 lowercase
  # hex characters from the project UUID before it makes the SES request.
  tenant_id_pattern             = "????????????????????????????????"
  customer_send_tenant_pattern  = "${local.customer_tenant_prefix}${local.tenant_id_pattern}"
  foreign_customer_send_pattern = var.env == "staging" ? "hedvi-production-*" : "hedvi-${local.tenant_id_pattern}"
}
data "aws_iam_policy_document" "environment_guard" {
  statement {
    sid       = "DenyForeignEnvironmentSESResources"
    effect    = "Deny"
    actions   = ["ses:*"]
    resources = ["${local.ses_arn}:*"]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/environment"
      values   = [local.foreign_environment]
    }
  }
  dynamic "statement" {
    for_each = var.env == "staging" ? [1] : []
    content {
      # Legacy staging prefix hedvi-* overlaps production names. This deny is
      # required even on a new tenant before any ResourceTag exists.
      sid     = "DenyProductionSESNamespace"
      effect  = "Deny"
      actions = ["ses:*"]
      resources = [
        "${local.ses_arn}:tenant/hedvi-production-*",
        "${local.ses_arn}:configuration-set/hedvi-production*",
        "${local.ses_arn}:tenant/operator-hedvi-production-auth*",
        "${local.ses_arn}:configuration-set/operator-hedvi-production-auth",
      ]
    }
  }
  dynamic "statement" {
    for_each = var.env == "production" ? [1] : []
    content {
      sid     = "DenyStagingSESConfiguration"
      effect  = "Deny"
      actions = ["ses:*"]
      resources = [
        "${local.ses_arn}:configuration-set/hedvi-staging*",
        "${local.ses_arn}:tenant/operator-hedvi-staging-auth*",
        "${local.ses_arn}:configuration-set/operator-hedvi-staging-auth",
      ]
    }
  }
}
