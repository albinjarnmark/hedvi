variable "env" {
  type        = string
  description = "staging or production"
  validation {
    condition     = contains(["staging", "production"], var.env)
    error_message = "env must be staging or production."
  }
}

variable "name" {
  type    = string
  default = "hedvi"
}

variable "content_max_days" {
  type        = number
  default     = 7
  description = "Maximum content retention offered to customers (plan maximum)."
}

variable "max_queue_days" {
  type        = number
  default     = 3
  description = "Maximum time an accepted email may wait before it fails (HEDVI_MAX_QUEUE_TIME)."
}

variable "ops_alert_emails" {
  type        = list(string)
  default     = []
  description = "Addresses subscribed to the independent ops alarm topic (not sent through Hedvi itself)."
}

variable "monthly_budget_usd" {
  type    = number
  default = 100
}

variable "budget_alert_emails" {
  type    = list(string)
  default = []
}

variable "auth_mail_domain" {
  type        = string
  default     = ""
  description = "Optional dedicated operator-owned auth subdomain. Empty disables auth infrastructure; API/worker auth configuration must then be provisioned separately."
  validation {
    condition     = var.auth_mail_domain == "" || (length(var.auth_mail_domain) <= 209 && length(split(".", var.auth_mail_domain)) <= 10 && can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.){2,}[a-z]{2,63}$", var.auth_mail_domain)) && !endswith(var.auth_mail_domain, ".test") && !endswith(var.auth_mail_domain, ".invalid"))
    error_message = "auth_mail_domain must be a normalized dedicated subdomain, such as auth.example.com, or empty."
  }
}
variable "auth_mail_from" {
  type        = string
  default     = ""
  description = "Optional exact mailbox on auth_mail_domain. Defaults to accounts@<auth_mail_domain>."
  validation {
    condition     = var.auth_mail_from == "" || (var.auth_mail_domain != "" && var.auth_mail_from == "accounts@${var.auth_mail_domain}")
    error_message = "auth_mail_from must be empty or exactly accounts@<auth_mail_domain>."
  }
}

variable "ses_tenant_prefix" {
  type        = string
  default     = ""
  description = "Customer SES tenant prefix. Empty selects the deployed environment default; existing staging remains hedvi-, production is hedvi-production-."
  validation {
    condition     = var.ses_tenant_prefix == "" || var.ses_tenant_prefix == (var.env == "production" ? "hedvi-production-" : "hedvi-")
    error_message = "Use hedvi- for staging and hedvi-production- for production; other deployed prefixes are forbidden."
  }
}
