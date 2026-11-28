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
run "disabled_is_optional" {
  command = plan
  assert {
    condition     = length(aws_sesv2_tenant.auth) == 0 && length(aws_sesv2_email_identity.auth) == 0 && length(output.auth_mail_dns_records) == 0
    error_message = "Empty auth domain must not create auth resources."
  }
  assert {
    condition     = !contains(keys(output.env), "HEDVI_AUTH_MAIL_FROM")
    error_message = "Disabled auth must not emit a misleading active sender."
  }
}
run "auth_is_operator_owned" {
  command = plan
  variables { auth_mail_domain = "auth.example.com" }
  assert {
    condition     = aws_sesv2_tenant.auth[0].tenant_name == "operator-hedvi-staging-auth" && aws_sesv2_email_identity.auth[0].configuration_set_name == "operator-hedvi-staging-auth"
    error_message = "Auth identity must default to its operator-only configuration set and tenant namespace."
  }
  assert {
    condition     = toset(aws_sesv2_configuration_set.auth[0].suppression_options[0].suppressed_reasons) == toset(["BOUNCE", "COMPLAINT"]) && aws_sesv2_configuration_set.auth[0].delivery_options[0].tls_policy == "REQUIRE"
    error_message = "Auth requires provider suppression and TLS."
  }
  assert {
    condition     = output.env.HEDVI_AUTH_MAIL_FROM == "accounts@auth.example.com" && output.env.HEDVI_AUTH_MAIL_TENANT == "operator-hedvi-staging-auth" && output.env.HEDVI_AUTH_MAIL_CONFIGURATION_SET == "operator-hedvi-staging-auth"
    error_message = "Application sender config must match operator resources."
  }
  assert {
    condition     = alltrue([for p in [data.aws_iam_policy_document.api.source_policy_documents[0], data.aws_iam_policy_document.worker.source_policy_documents[0]] : length([for s in jsondecode(p).Statement : s if s.Sid == "DenyOperatorAuthNamespaceMutation" && s.Effect == "Deny" && contains(s.Resource, "arn:aws:ses:eu-north-1:123456789012:identity/example.com")]) == 1])
    error_message = "Both service roles must deny mutation of auth identity and ancestors."
  }
  assert {
    condition     = length([for s in data.aws_iam_policy_document.api.statement : s if s.effect != "Deny" && (contains(s.actions, "ses:SendEmail") || contains(s.actions, "ses:SendRawEmail"))]) == 0
    error_message = "Public API must never get an auth-send allow."
  }
  assert {
    condition     = length([for s in data.aws_iam_policy_document.api.statement : s if s.effect == "Deny" && contains(s.actions, "ses:Send*") && contains(s.resources, "*")]) == 1
    error_message = "The API must explicitly deny SES sending even if another role policy adds an allow."
  }
  assert {
    condition     = length([for s in data.aws_iam_policy_document.worker.statement : s if s.sid == "SendOperatorAuthMail" && toset(s.resources) == toset(["arn:aws:ses:eu-north-1:123456789012:identity/auth.example.com", "arn:aws:ses:eu-north-1:123456789012:configuration-set/operator-hedvi-staging-auth"]) && length([for c in s.condition : c if c.test == "StringEquals" && ((c.variable == "ses:FromAddress" && toset(c.values) == toset(["accounts@auth.example.com"])) || (c.variable == "ses:TenantName" && toset(c.values) == toset(["operator-hedvi-staging-auth"])))]) == 2]) == 1
    error_message = "Auth send must bind exact mailbox, tenant and identity/configuration resources."
  }
  assert {
    condition = one([
      for s in data.aws_iam_policy_document.worker.statement : (
        length(s.condition) == 3 && toset(s.actions) == toset(["ses:SendRawEmail"]) &&
        length([for c in s.condition : c if c.test == "StringLike" && c.variable == "ses:TenantName" && toset(c.values) == toset(["hedvi-${join("", [for i in range(32) : "?"])}"])]) == 1 &&
        length([for c in s.condition : c if c.test == "StringNotEqualsIgnoreCase" && c.variable == "ses:FromAddress" && toset(c.values) == toset(["accounts@auth.example.com"])]) == 1 &&
        length([for c in s.condition : c if c.test == "StringNotLike" && c.variable == "ses:FromAddress" && toset(c.values) == toset(["*@auth.example.com", "*@*.auth.example.com"])]) == 1
      ) if s.sid == "SendCustomerRaw"
    ])
    error_message = "Customer RAW must exclude the operator mailbox, its domain and subdomains even if SES authorizes a parent identity."
  }
  assert {
    condition = toset([
      for s in jsondecode(data.aws_iam_policy_document.worker.json).Statement : s.Sid
      if s.Effect == "Allow" && (can(s.NotAction) || anytrue([for a in flatten([try(s.Action, [])]) : can(regex("^(ses:send|ses:\\*|\\*)", lower(a)))]))
    ]) == toset(["SendCustomerRaw", "SendOperatorAuthMail"])
    error_message = "A third grant must not enable Simple, templated or unscoped sending."
  }
  assert {
    condition = one([
      for s in jsondecode(data.aws_iam_policy_document.worker.json).Statement : (
        s.Effect == "Deny" && s.Resource == "*" && toset(s.Action) == toset(["ses:SendEmail", "ses:SendRawEmail"]) &&
        s.Condition.StringEqualsIgnoreCase["ses:FromAddress"] == "accounts@auth.example.com" &&
        s.Condition.StringNotEquals["ses:TenantName"] == "operator-hedvi-staging-auth"
      ) if s.Sid == "DenyAuthMailboxOutsideOperatorTenant"
    ])
    error_message = "The dedicated auth mailbox must remain denied outside its exact operator tenant even with other grants."
  }
}
run "reject_bare_domain" {
  command = plan
  variables { auth_mail_domain = "example.com" }
  expect_failures = [var.auth_mail_domain]
}
run "reject_unrelated_mailbox" {
  command = plan
  variables {
    auth_mail_domain = "auth.example.com"
    auth_mail_from   = "other@example.com"
  }
  expect_failures = [var.auth_mail_from]
}
