# Compute and stateful services for Hedvi: VPC over two AZs, ECR, RDS
# PostgreSQL 18 (Multi-AZ optional), ElastiCache Valkey, ALB with host
# routing, ECS Fargate services (api, worker, web) and Secrets Manager.
terraform {
  required_version = ">= 1.11"
  required_providers {
    aws = { source = "hashicorp/aws", version = "6.73.0" }
  }
}

variable "env" { type = string }
variable "name" {
  type    = string
  default = "hedvi"
}
variable "vpc_cidr" {
  type    = string
  default = "10.40.0.0/16"
}
variable "nat_gateways" {
  type        = number
  default     = 1
  description = "1 for staging, 2 for production (one per AZ)."
}
variable "db_instance_class" {
  type    = string
  default = "db.t4g.small"
}
variable "db_multi_az" {
  type    = bool
  default = false
}
variable "postgres_version" {
  type        = string
  default     = "18.1"
  description = "Migrations use uuidv7(), which requires PostgreSQL 18."
}
variable "valkey_node_type" {
  type    = string
  default = "cache.t4g.micro"
}
variable "valkey_auth_token" {
  type        = string
  sensitive   = true
  ephemeral   = true
  description = "Provide the same secret to plan and apply. Only write-only provider arguments receive it."
  validation {
    condition     = can(regex("^[a-zA-Z0-9!&#$^<>-]{16,128}$", var.valkey_auth_token))
    error_message = "Use a 16-128 character ElastiCache AUTH token. A cryptographically random 64-character hexadecimal string is supported."
  }
}
variable "valkey_auth_token_version" {
  type    = number
  default = 1
  validation {
    condition     = var.valkey_auth_token_version >= 1 && floor(var.valkey_auth_token_version) == var.valkey_auth_token_version
    error_message = "Use a positive integer and increase it each time the token changes."
  }
}
variable "valkey_auth_token_update_strategy" {
  type    = string
  default = "SET"
  validation {
    condition     = contains(["SET", "ROTATE"], var.valkey_auth_token_update_strategy)
    error_message = "Use ROTATE during migration or rotation and SET to require the current token."
  }
}
variable "enable_ops_webhook" {
  type        = bool
  default     = false
  description = "Inject a separately provisioned worker webhook secret when using a webhook instead of SNS."
}
variable "api_domain" { type = string }
variable "app_domain" { type = string }
variable "site_domain" { type = string }
variable "certificate_arn" {
  type        = string
  description = "ACM certificate in eu-north-1 covering the domains above."
}
variable "image_tag" {
  type    = string
  default = "latest"
}
variable "api_count" {
  type    = number
  default = 2
}
variable "worker_count" {
  type    = number
  default = 2
}
variable "web_count" {
  type    = number
  default = 2
}
variable "app_env" {
  type        = map(string)
  description = "Non-secret Hedvi environment from module hedvi (output env)."
}
variable "api_policy_arn" { type = string }
variable "worker_policy_arn" { type = string }

data "aws_availability_zones" "azs" { state = "available" }
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

locals {
  prefix = "${var.name}-${var.env}"
  azs    = slice(data.aws_availability_zones.azs.names, 0, 2)
  tags   = { project = var.name, environment = var.env, managed_by = "terraform" }
}

# ---------------------------------------------------------------------------
# Network
# ---------------------------------------------------------------------------

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags                 = merge(local.tags, { Name = local.prefix })
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
  tags   = local.tags
}

resource "aws_subnet" "public" {
  count                   = 2
  vpc_id                  = aws_vpc.main.id
  availability_zone       = local.azs[count.index]
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, count.index)
  map_public_ip_on_launch = false
  tags                    = merge(local.tags, { Name = "${local.prefix}-public-${count.index}" })
}

resource "aws_subnet" "private" {
  count             = 2
  vpc_id            = aws_vpc.main.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, 10 + count.index)
  tags              = merge(local.tags, { Name = "${local.prefix}-private-${count.index}" })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
  tags = local.tags
}

resource "aws_route_table_association" "public" {
  count          = 2
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_eip" "nat" {
  count  = var.nat_gateways
  domain = "vpc"
  tags   = local.tags
}

# Workers need egress for customer webhooks and DNS checks.
resource "aws_nat_gateway" "main" {
  count         = var.nat_gateways
  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id
  tags          = local.tags
}

resource "aws_route_table" "private" {
  count  = 2
  vpc_id = aws_vpc.main.id
  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.main[min(count.index, var.nat_gateways - 1)].id
  }
  tags = local.tags
}

resource "aws_route_table_association" "private" {
  count          = 2
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[count.index].id
}

# Keep S3 traffic off the NAT gateway.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.main.id
  service_name      = "com.amazonaws.${data.aws_region.current.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = aws_route_table.private[*].id
  tags              = local.tags
}

# ---------------------------------------------------------------------------
# Security groups
# ---------------------------------------------------------------------------

resource "aws_security_group" "alb" {
  name   = "${local.prefix}-alb"
  vpc_id = aws_vpc.main.id
  tags   = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "alb_public" {
  for_each          = toset(["80", "443"])
  security_group_id = aws_security_group.alb.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = tonumber(each.key)
  to_port           = tonumber(each.key)
}
resource "aws_security_group" "task" {
  for_each = toset(["web", "api", "worker", "migrate"])
  name     = "${local.prefix}-${each.key}-tasks"
  vpc_id   = aws_vpc.main.id
  tags     = local.tags
}

locals {
  targets = { web = 3030, api = 8780 }
}

resource "aws_vpc_security_group_egress_rule" "alb_targets" {
  for_each                     = local.targets
  security_group_id            = aws_security_group.alb.id
  referenced_security_group_id = aws_security_group.task[each.key].id
  ip_protocol                  = "tcp"
  from_port                    = each.value
  to_port                      = each.value
}
resource "aws_vpc_security_group_ingress_rule" "alb_targets" {
  for_each                     = local.targets
  security_group_id            = aws_security_group.task[each.key].id
  referenced_security_group_id = aws_security_group.alb.id
  ip_protocol                  = "tcp"
  from_port                    = each.value
  to_port                      = each.value
}

# ECR, CloudWatch, Secrets Manager, S3, SES, SNS and the web proxy use HTTPS.
# Customer webhooks may specify their own TLS port, which netsafe validates.
resource "aws_vpc_security_group_egress_rule" "https" {
  for_each          = toset(["web", "api", "migrate"])
  security_group_id = aws_security_group.task[each.key].id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}
resource "aws_vpc_security_group_egress_rule" "worker" {
  security_group_id = aws_security_group.task["worker"].id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 1
  to_port           = 65535
}
resource "aws_security_group" "database" {
  name   = "${local.prefix}-database"
  vpc_id = aws_vpc.main.id
  tags   = local.tags
}
resource "aws_security_group" "cache" {
  name   = "${local.prefix}-cache"
  vpc_id = aws_vpc.main.id
  tags   = local.tags
}
resource "aws_vpc_security_group_ingress_rule" "database" {
  for_each                     = toset(["api", "worker", "migrate"])
  security_group_id            = aws_security_group.database.id
  referenced_security_group_id = aws_security_group.task[each.key].id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}
resource "aws_vpc_security_group_egress_rule" "database" {
  for_each                     = toset(["api", "migrate"])
  security_group_id            = aws_security_group.task[each.key].id
  referenced_security_group_id = aws_security_group.database.id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}
resource "aws_vpc_security_group_ingress_rule" "cache" {
  for_each                     = toset(["api", "worker"])
  security_group_id            = aws_security_group.cache.id
  referenced_security_group_id = aws_security_group.task[each.key].id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}
resource "aws_vpc_security_group_egress_rule" "api_cache" {
  security_group_id            = aws_security_group.task["api"].id
  referenced_security_group_id = aws_security_group.cache.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

# ---------------------------------------------------------------------------
# PostgreSQL and Valkey
# ---------------------------------------------------------------------------

resource "aws_db_subnet_group" "main" {
  name       = local.prefix
  subnet_ids = aws_subnet.private[*].id
  tags       = local.tags
}

resource "aws_db_parameter_group" "pg" {
  name   = "${local.prefix}-pg18"
  family = "postgres18"
  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
  dynamic "parameter" {
    for_each = {
      log_min_duration_statement        = "-1"
      log_min_duration_sample           = "-1"
      log_statement                     = "none"
      log_min_error_statement           = "panic"
      log_parameter_max_length          = "0"
      log_parameter_max_length_on_error = "0"
      log_error_verbosity               = "terse"
    }
    content {
      name  = parameter.key
      value = parameter.value
    }
  }
  tags = local.tags
}

resource "aws_db_instance" "main" {
  identifier                   = local.prefix
  engine                       = "postgres"
  engine_version               = var.postgres_version
  instance_class               = var.db_instance_class
  allocated_storage            = 20
  max_allocated_storage        = 200
  storage_type                 = "gp3"
  storage_encrypted            = true
  db_name                      = "hedvi"
  username                     = "hedvi_master"
  manage_master_user_password  = true
  multi_az                     = var.db_multi_az
  db_subnet_group_name         = aws_db_subnet_group.main.name
  vpc_security_group_ids       = [aws_security_group.database.id]
  parameter_group_name         = aws_db_parameter_group.pg.name
  backup_retention_period      = 14
  backup_window                = "01:00-02:00"
  maintenance_window           = "sun:02:30-sun:03:30"
  deletion_protection          = var.env == "production"
  skip_final_snapshot          = var.env != "production"
  final_snapshot_identifier    = var.env == "production" ? "${local.prefix}-final" : null
  performance_insights_enabled = false
  auto_minor_version_upgrade   = true
  copy_tags_to_snapshot        = true
  tags                         = local.tags
}

resource "aws_elasticache_subnet_group" "main" {
  name       = local.prefix
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_elasticache_replication_group" "valkey" {
  replication_group_id       = local.prefix
  description                = "Hedvi rate limiting (not a source of truth)"
  engine                     = "valkey"
  engine_version             = "8.0"
  node_type                  = var.valkey_node_type
  num_cache_clusters         = var.env == "production" ? 2 : 1
  automatic_failover_enabled = var.env == "production"
  subnet_group_name          = aws_elasticache_subnet_group.main.name
  security_group_ids         = [aws_security_group.cache.id]
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  auth_token_wo              = var.valkey_auth_token
  # The provider reads its write-only token on updates only when this marker
  # changes. The ROTATE -> SET transition must resend the same token.
  auth_token_wo_version      = var.valkey_auth_token_version * 2 + (var.valkey_auth_token_update_strategy == "SET" ? 1 : 0)
  auth_token_update_strategy = var.valkey_auth_token_update_strategy
  tags                       = local.tags
}

# ---------------------------------------------------------------------------
# Operators populate the application secrets. Terraform sends the Valkey URL
# directly to Secrets Manager and retains only the version marker.
# ---------------------------------------------------------------------------

locals {
  secret_names = {
    api     = ["HEDVI_DATABASE_URL_API", "HEDVI_DATA_KEY", "HEDVI_API_KEY_PEPPER", "HEDVI_VALKEY_URL", "HEDVI_PROXY_ATTESTATION_KEY"]
    worker  = concat(["HEDVI_DATABASE_URL_WORKER", "HEDVI_DATA_KEY", "HEDVI_API_KEY_PEPPER", "HEDVI_VALKEY_URL"], var.enable_ops_webhook ? ["HEDVI_OPS_ALERT_WEBHOOK_URL"] : [])
    migrate = ["HEDVI_DATABASE_URL_MIGRATE", "HEDVI_DATA_KEY", "HEDVI_API_KEY_PEPPER"]
    web     = ["HEDVI_PROXY_ATTESTATION_KEY"]
  }
  secrets = toset(concat(flatten(values(local.secret_names)), ["HEDVI_OPS_ALERT_WEBHOOK_URL"]))
}

resource "aws_secretsmanager_secret" "app" {
  for_each                = toset(local.secrets)
  name                    = "${local.prefix}/${each.key}"
  recovery_window_in_days = var.env == "production" ? 30 : 0
  tags                    = local.tags
}

resource "aws_secretsmanager_secret_version" "valkey_url" {
  secret_id                = aws_secretsmanager_secret.app["HEDVI_VALKEY_URL"].id
  secret_string_wo         = "rediss://:${urlencode(var.valkey_auth_token)}@${aws_elasticache_replication_group.valkey.primary_endpoint_address}:6379/0"
  secret_string_wo_version = var.valkey_auth_token_version
  lifecycle {
    replace_triggered_by = [aws_elasticache_replication_group.valkey.primary_endpoint_address]
  }
}

# ---------------------------------------------------------------------------
# Images, logs, IAM
# ---------------------------------------------------------------------------

resource "aws_ecr_repository" "repo" {
  for_each             = toset(["api", "web"])
  name                 = "${local.prefix}-${each.key}"
  image_tag_mutability = "IMMUTABLE"
  image_scanning_configuration { scan_on_push = true }
  encryption_configuration { encryption_type = "KMS" }
  tags = local.tags
}

resource "aws_cloudwatch_log_group" "svc" {
  for_each          = toset(["api", "worker", "web"])
  name              = "/${local.prefix}/${each.key}"
  retention_in_days = 30
  tags              = local.tags
}

data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  for_each           = local.secret_names
  name               = "${local.prefix}-${each.key}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}
resource "aws_iam_role_policy_attachment" "execution" {
  for_each   = aws_iam_role.execution
  role       = each.value.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}
resource "aws_iam_role_policy" "execution_secrets" {
  for_each = local.secret_names
  role     = aws_iam_role.execution[each.key].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["secretsmanager:GetSecretValue"]
      Resource = [for key in each.value : aws_secretsmanager_secret.app[key].arn]
    }]
  })
}

resource "aws_iam_role" "task" {
  for_each           = toset(["api", "worker", "web"])
  name               = "${local.prefix}-${each.key}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy_attachment" "api" {
  role       = aws_iam_role.task["api"].name
  policy_arn = var.api_policy_arn
}

resource "aws_iam_role_policy_attachment" "worker" {
  role       = aws_iam_role.task["worker"].name
  policy_arn = var.worker_policy_arn
}

# ---------------------------------------------------------------------------
# Load balancer with host routing
# ---------------------------------------------------------------------------

# Application Load Balancer access logging supports SSE-S3. Keep the
# logs in a separate private bucket with short retention.
resource "aws_s3_bucket" "alb_logs" {
  bucket = "${local.prefix}-alb-logs-${data.aws_caller_identity.current.account_id}"
  tags   = local.tags
}
resource "aws_s3_bucket_public_access_block" "alb_logs" {
  bucket                  = aws_s3_bucket.alb_logs.id
  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_ownership_controls" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id
  rule { object_ownership = "BucketOwnerEnforced" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_s3_bucket_lifecycle_configuration" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id
  rule {
    id     = "expire-access-logs"
    status = "Enabled"
    filter {}
    expiration { days = 30 }
  }
}
resource "aws_s3_bucket_policy" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "DenyInsecureTransport", Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [aws_s3_bucket.alb_logs.arn, "${aws_s3_bucket.alb_logs.arn}/*"], Condition = { Bool = { "aws:SecureTransport" = "false" } } },
      { Sid      = "AllowALBLogDelivery", Effect = "Allow", Principal = { Service = "logdelivery.elasticloadbalancing.amazonaws.com" }, Action = "s3:PutObject",
        Resource = "${aws_s3_bucket.alb_logs.arn}/AWSLogs/${data.aws_caller_identity.current.account_id}/*",
      Condition = { ArnLike = { "aws:SourceArn" = "arn:aws:elasticloadbalancing:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:loadbalancer/app/${local.prefix}/*" } } }
    ]
  })
}
resource "aws_lb" "main" {
  name                       = local.prefix
  load_balancer_type         = "application"
  subnets                    = aws_subnet.public[*].id
  security_groups            = [aws_security_group.alb.id]
  drop_invalid_header_fields = true
  enable_deletion_protection = var.env == "production"
  xff_header_processing_mode = "append"
  access_logs {
    bucket  = aws_s3_bucket.alb_logs.id
    enabled = true
  }
  depends_on   = [aws_s3_bucket_policy.alb_logs]
  idle_timeout = 65
  tags         = local.tags
}

resource "aws_lb_target_group" "api" {
  name        = "${local.prefix}-api"
  port        = 8780
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = aws_vpc.main.id
  health_check {
    path    = "/readyz"
    matcher = "200"
  }
  deregistration_delay = 30
}

resource "aws_lb_target_group" "web" {
  name        = "${local.prefix}-web"
  port        = 3030
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = aws_vpc.main.id
  health_check {
    path    = "/"
    matcher = "200"
  }
  deregistration_delay = 30
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.main.arn
  port              = 80
  protocol          = "HTTP"
  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      status_code  = "404"
    }
  }
}

resource "aws_lb_listener_rule" "redirect" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 10
  condition {
    host_header { values = [var.site_domain, var.app_domain, var.api_domain] }
  }
  action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.main.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn
  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      status_code  = "404"
    }
  }
}

resource "aws_lb_listener_rule" "api" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 10
  condition {
    host_header { values = [var.api_domain] }
  }
  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}

resource "aws_lb_listener_rule" "web" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 20
  condition {
    host_header { values = [var.site_domain, var.app_domain] }
  }
  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.web.arn
  }
}

# ---------------------------------------------------------------------------
# ECS
# ---------------------------------------------------------------------------

resource "aws_ecs_cluster" "main" {
  name = local.prefix
  setting {
    name  = "containerInsights"
    value = "enabled"
  }
  tags = local.tags
}

locals {
  common_env = merge(var.app_env, {
    HEDVI_ENV                     = var.env
    HEDVI_MOCK_DNS                = "false"
    HEDVI_COOKIE_SECURE           = "true"
    HEDVI_PUBLIC_API_URL          = "https://${var.api_domain}"
    HEDVI_APP_URL                 = "https://${var.app_domain}"
    HEDVI_LEGAL_ORIGIN            = "https://${var.site_domain}"
    HEDVI_ALLOWED_ORIGINS         = "https://${var.app_domain},https://${var.site_domain}"
    HEDVI_TRUSTED_PROXIES         = join(",", aws_subnet.public[*].cidr_block)
    HEDVI_OPS_ALERT_SNS_TOPIC_ARN = var.enable_ops_webhook ? "" : lookup(var.app_env, "HEDVI_OPS_ALERT_SNS_TOPIC_ARN", "")
    HEDVI_API_ADDR                = "0.0.0.0:8780"
    HEDVI_METRICS_ADDR            = "127.0.0.1:9464"
  })
  env_list = [for k, v in local.common_env : { name = k, value = v }]
  secret_lists = { for service, keys in local.secret_names : service => [for k in keys : {
    name = k
    # Pin the managed URL so a rotation rolls API/worker tasks onto its new version.
    valueFrom = k == "HEDVI_VALKEY_URL" ? "${aws_secretsmanager_secret.app[k].arn}:::${aws_secretsmanager_secret_version.valkey_url.version_id}" : aws_secretsmanager_secret.app[k].arn
  }] }
  web_env = {
    HEDVI_API_INTERNAL_URL = "https://${var.api_domain}"
    HEDVI_PUBLIC_API_URL   = "https://${var.api_domain}"
    HEDVI_INGRESS_PROVIDER = "alb"
    HEDVI_APP_HOST         = var.app_domain
    HEDVI_SITE_URL         = "https://${var.site_domain}"
  }
  services = {
    api    = { image = "api", command = ["api"], port = 8780, cpu = 512, memory = 1024, count = var.api_count }
    worker = { image = "api", command = ["worker"], port = 0, cpu = 512, memory = 1024, count = var.worker_count }
    web    = { image = "web", command = [], port = 3030, cpu = 256, memory = 512, count = var.web_count }
  }
}

resource "aws_ecs_task_definition" "svc" {
  for_each                 = local.services
  family                   = "${local.prefix}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  execution_role_arn       = aws_iam_role.execution[each.key].arn
  task_role_arn            = aws_iam_role.task[each.key].arn
  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }
  container_definitions = jsonencode([{
    name                   = each.key
    image                  = "${aws_ecr_repository.repo[each.value.image].repository_url}:${var.image_tag}"
    command                = length(each.value.command) > 0 ? each.value.command : null
    essential              = true
    portMappings           = each.value.port > 0 ? [{ containerPort = each.value.port, protocol = "tcp" }] : []
    environment            = each.key == "web" ? [for k, v in local.web_env : { name = k, value = v }] : local.env_list
    secrets                = local.secret_lists[each.key]
    readonlyRootFilesystem = each.key != "web"
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.svc[each.key].name
        awslogs-region        = data.aws_region.current.region
        awslogs-stream-prefix = each.key
      }
    }
  }])
  tags = local.tags
}

resource "aws_ecs_service" "svc" {
  for_each               = local.services
  name                   = each.key
  cluster                = aws_ecs_cluster.main.id
  task_definition        = aws_ecs_task_definition.svc[each.key].arn
  desired_count          = each.value.count
  launch_type            = "FARGATE"
  enable_execute_command = false
  propagate_tags         = "SERVICE"
  network_configuration {
    subnets         = aws_subnet.private[*].id
    security_groups = [aws_security_group.task[each.key].id]
  }
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
  dynamic "load_balancer" {
    for_each = each.value.port > 0 ? [1] : []
    content {
      target_group_arn = each.key == "api" ? aws_lb_target_group.api.arn : aws_lb_target_group.web.arn
      container_name   = each.key
      container_port   = each.value.port
    }
  }
  depends_on = [aws_lb_listener_rule.api, aws_lb_listener_rule.web, aws_iam_role_policy.execution_secrets, aws_iam_role_policy_attachment.execution, aws_secretsmanager_secret_version.valkey_url]
}

output "alb_dns_name" { value = aws_lb.main.dns_name }
output "ecr_repositories" { value = { for k, r in aws_ecr_repository.repo : k => r.repository_url } }
output "database_endpoint" { value = aws_db_instance.main.address }
output "database_master_secret_arn" { value = aws_db_instance.main.master_user_secret[0].secret_arn }
output "secrets" { value = { for k, s in aws_secretsmanager_secret.app : k => s.arn } }
output "cluster" { value = aws_ecs_cluster.main.name }
output "private_subnets" { value = aws_subnet.private[*].id }
output "migration_security_group" { value = aws_security_group.task["migrate"].id }

# The owner credential is injected only into a deliberately started migration
# task, never into a continuously running API or worker container.
resource "aws_ecs_task_definition" "migrate" {
  family                   = "${local.prefix}-migrate"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 512
  memory                   = 1024
  execution_role_arn       = aws_iam_role.execution["migrate"].arn
  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }
  container_definitions = jsonencode([{ name = "migrate", image = "${aws_ecr_repository.repo["api"].repository_url}:${var.image_tag}", command = ["migrate"], essential = true,
    environment = local.env_list, secrets = local.secret_lists["migrate"], readonlyRootFilesystem = true,
  logConfiguration = { logDriver = "awslogs", options = { awslogs-group = aws_cloudwatch_log_group.svc["worker"].name, awslogs-region = data.aws_region.current.region, awslogs-stream-prefix = "migrate" } } }])
  tags = local.tags
}
output "migration_task_definition" { value = aws_ecs_task_definition.migrate.arn }
