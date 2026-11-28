# Plan-only, no credentials and no network. Provider policy-document rendering is
# real; identity/region are overridden and resource ARNs are deterministic.
provider "aws" {
  region                      = "eu-north-1"
  access_key                  = "local-test-only"
  secret_key                  = "local-test-only"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  skip_region_validation      = true
}
override_data {
  override_during = plan
  target          = data.aws_caller_identity.current
  values          = { account_id = "123456789012", arn = "arn:aws:iam::123456789012:root", user_id = "local-test" }
}
override_data {
  override_during = plan
  target          = data.aws_region.current
  values          = { region = "eu-north-1", name = "eu-north-1" }
}
override_resource {
  override_during = plan
  target          = aws_kms_key.data
  values          = { arn = "arn:aws:kms:eu-north-1:123456789012:key/local-test", key_id = "local-test" }
}
override_resource {
  override_during = plan
  target          = aws_s3_bucket.payloads
  values          = { arn = "arn:aws:s3:::hedvi-staging-payloads-123456789012", id = "hedvi-staging-payloads-123456789012" }
}
override_resource {
  override_during = plan
  target          = aws_sns_topic.ops
  values          = { arn = "arn:aws:sns:eu-north-1:123456789012:hedvi-staging-ops" }
}
override_resource {
  override_during = plan
  target          = aws_sns_topic.ses_events
  values          = { arn = "arn:aws:sns:eu-north-1:123456789012:hedvi-staging-ses-events" }
}
override_resource {
  override_during = plan
  target          = aws_sqs_queue.main
  values          = { arn = "arn:aws:sqs:eu-north-1:123456789012:hedvi-staging-queue", url = "https://sqs.eu-north-1.amazonaws.com/123456789012/hedvi-staging-queue" }
}
variables { env = "staging" }
run "legacy_staging_excludes_production" {
  command = plan
  assert {
    condition     = output.env.HEDVI_SES_TENANT_PREFIX == "hedvi-"
    error_message = "Existing staging tenant names must not change."
  }
  assert {
    condition = one([
      for s in data.aws_iam_policy_document.worker.statement : (
        s.effect == "Allow" && toset(s.actions) == toset(["ses:SendRawEmail"]) &&
        toset(s.resources) == toset(["arn:aws:ses:eu-north-1:123456789012:identity/*", "arn:aws:ses:eu-north-1:123456789012:configuration-set/hedvi-staging"]) &&
        one([for c in s.condition : c.test == "StringLike" && c.variable == "ses:TenantName" && toset(c.values) == toset(["hedvi-${join("", [for i in range(32) : "?"])}"])])
      ) if s.sid == "SendCustomerRaw"
    ])
    error_message = "Raw mail requires exactly one 32-character staging tenant and the staging identity/configuration resources. A resource-tag condition breaks real MIME sends."
  }
  assert {
    condition = one([
      for s in data.aws_iam_policy_document.worker.statement : (
        s.effect == "Deny" && toset(s.resources) == toset(["*"]) && toset(s.actions) == toset(["ses:SendEmail", "ses:SendRawEmail"]) &&
        one([for c in s.condition : c.test == "StringLike" && c.variable == "ses:TenantName" && toset(c.values) == toset(["hedvi-production-*", "operator-hedvi-production-auth"])])
      ) if s.sid == "DenyForeignSendTenant"
    ])
    error_message = "Both send actions must explicitly deny production tenant names even when authorizing an identity ARN with no tag context."
  }
  assert {
    condition     = alltrue([for p in [data.aws_iam_policy_document.api.source_policy_documents[1], data.aws_iam_policy_document.worker.source_policy_documents[1]] : length([for s in jsondecode(p).Statement : s if s.Sid == "DenyProductionSESNamespace" && s.Effect == "Deny" && contains(s.Resource, "arn:aws:ses:eu-north-1:123456789012:tenant/hedvi-production-*")]) == 1])
    error_message = "The legacy staging wildcard must explicitly deny production tenants in both roles."
  }
  assert {
    condition     = alltrue([for s in concat(tolist(data.aws_iam_policy_document.api.statement), tolist(data.aws_iam_policy_document.worker.statement)) : !contains(s.actions, "ses:CreateTenantResourceAssociation") || length([for c in s.condition : c if c.test == "StringEquals" && c.variable == "aws:ResourceTag/environment" && toset(c.values) == toset(["staging"])]) == 1])
    error_message = "Association permissions must require the right environment on every authorized resource kind."
  }
  assert {
    condition     = alltrue([for s in concat(tolist(data.aws_iam_policy_document.api.statement), tolist(data.aws_iam_policy_document.worker.statement)) : !contains(s.actions, "ses:TagResource") || length([for c in s.condition : c if c.test == "StringEquals" && c.variable == "aws:RequestTag/environment" && toset(c.values) == toset(["staging"])]) == 1])
    error_message = "Tag creation must not allow a different environment."
  }
}
run "production_uses_only_production_tenants" {
  command = plan
  variables { env = "production" }
  assert {
    condition     = output.env.HEDVI_SES_TENANT_PREFIX == "hedvi-production-" && alltrue([for s in concat(tolist(data.aws_iam_policy_document.api.statement), tolist(data.aws_iam_policy_document.worker.statement)) : !contains(s.actions, "ses:CreateTenant") || toset(s.resources) == toset(["arn:aws:ses:eu-north-1:123456789012:tenant/hedvi-production-*"])])
    error_message = "Production must not allocate or mutate legacy staging tenants."
  }
  assert {
    condition = one([
      for s in data.aws_iam_policy_document.worker.statement : (
        s.effect == "Allow" && toset(s.actions) == toset(["ses:SendRawEmail"]) &&
        toset(s.resources) == toset(["arn:aws:ses:eu-north-1:123456789012:identity/*", "arn:aws:ses:eu-north-1:123456789012:configuration-set/hedvi-production"]) &&
        one([for c in s.condition : c.test == "StringLike" && c.variable == "ses:TenantName" && toset(c.values) == toset(["hedvi-production-${join("", [for i in range(32) : "?"])}"])])
      ) if s.sid == "SendCustomerRaw"
    ])
    error_message = "Production RAW must require the exact tenant shape, local identity resources and only the production customer configuration set."
  }
  assert {
    condition = one([
      for s in data.aws_iam_policy_document.worker.statement : (
        s.effect == "Deny" && toset(s.resources) == toset(["*"]) && toset(s.actions) == toset(["ses:SendEmail", "ses:SendRawEmail"]) &&
        one([for c in s.condition : c.test == "StringLike" && c.variable == "ses:TenantName" && toset(c.values) == toset(["hedvi-${join("", [for i in range(32) : "?"])}", "operator-hedvi-staging-auth"])])
      ) if s.sid == "DenyForeignSendTenant"
    ])
    error_message = "Production must deny staging customer and operator tenants on all send resources."
  }
  assert {
    condition     = alltrue([for p in [data.aws_iam_policy_document.api.source_policy_documents[1], data.aws_iam_policy_document.worker.source_policy_documents[1]] : length([for s in jsondecode(p).Statement : s if s.Sid == "DenyForeignEnvironmentSESResources" && s.Effect == "Deny" && s.Condition.StringEquals["aws:ResourceTag/environment"] == "staging"]) == 1])
    error_message = "Production must explicitly deny tagged staging resources, including retagging."
  }
  assert {
    condition     = alltrue([for s in concat(tolist(data.aws_iam_policy_document.api.statement), tolist(data.aws_iam_policy_document.worker.statement)) : !contains(s.actions, "ses:GetTenant") || length(s.condition) == 0])
    error_message = "GetTenant must be able to observe NotFound before resource tags exist."
  }
}
run "reject_production_legacy_prefix" {
  command = plan
  variables {
    env               = "production"
    ses_tenant_prefix = "hedvi-"
  }
  expect_failures = [var.ses_tenant_prefix]
}
