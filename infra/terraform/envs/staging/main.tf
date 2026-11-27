terraform {
  required_version = ">= 1.11"
  required_providers {
    aws = { source = "hashicorp/aws", version = "6.73.0" }
  }
  # State bucket and owner are supplied with -backend-config=backend.hcl.
  backend "s3" {
    key          = "hedvi/staging.tfstate"
    region       = "eu-north-1"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = "eu-north-1"
  default_tags {
    tags = { project = "hedvi", environment = "staging" }
  }
}

variable "ops_alert_emails" {
  type    = list(string)
  default = []
}

variable "budget_alert_emails" {
  type    = list(string)
  default = []
}

variable "auth_mail_domain" {
  type    = string
  default = ""
}
variable "auth_mail_from" {
  type    = string
  default = ""
}

module "hedvi" {
  auth_mail_domain    = var.auth_mail_domain
  auth_mail_from      = var.auth_mail_from
  source              = "../../modules/hedvi"
  env                 = "staging"
  monthly_budget_usd  = 50
  ops_alert_emails    = var.ops_alert_emails
  budget_alert_emails = var.budget_alert_emails
}

variable "api_domain" { type = string }
variable "app_domain" { type = string }
variable "site_domain" { type = string }

variable "certificate_arn" {
  type        = string
  description = "ACM certificate (eu-north-1) for the domains below"
}

variable "valkey_auth_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "valkey_auth_token_version" {
  type    = number
  default = 1
}

variable "valkey_auth_token_update_strategy" {
  type    = string
  default = "SET"
}

variable "enable_ops_webhook" {
  type    = bool
  default = false
}

variable "api_count" {
  type    = number
  default = 1
}

variable "worker_count" {
  type    = number
  default = 1
}

variable "web_count" {
  type    = number
  default = 1
}

variable "image_tag" {
  type    = string
  default = "latest"
}

module "platform" {
  source                            = "../../modules/platform"
  valkey_auth_token                 = var.valkey_auth_token
  valkey_auth_token_version         = var.valkey_auth_token_version
  valkey_auth_token_update_strategy = var.valkey_auth_token_update_strategy
  enable_ops_webhook                = var.enable_ops_webhook
  env                               = "staging"
  api_domain                        = var.api_domain
  app_domain                        = var.app_domain
  site_domain                       = var.site_domain
  certificate_arn                   = var.certificate_arn
  image_tag                         = var.image_tag
  nat_gateways                      = 1
  db_multi_az                       = false
  db_instance_class                 = "db.t4g.small"
  api_count                         = var.api_count
  worker_count                      = var.worker_count
  web_count                         = var.web_count
  app_env                           = module.hedvi.env
  api_policy_arn                    = module.hedvi.api_policy_arn
  worker_policy_arn                 = module.hedvi.worker_policy_arn
}

output "env" {
  value = module.hedvi.env
}

output "platform" {
  value = {
    private_subnets           = module.platform.private_subnets
    migration_security_group  = module.platform.migration_security_group
    migration_task_definition = module.platform.migration_task_definition
    alb_dns_name              = module.platform.alb_dns_name
    ecr                       = module.platform.ecr_repositories
    database                  = module.platform.database_endpoint
    cluster                   = module.platform.cluster
    secrets                   = module.platform.secrets
  }
}

output "auth_mail_dns_records" { value = module.hedvi.auth_mail_dns_records }
output "auth_mail_resources" { value = module.hedvi.auth_mail_resources }
