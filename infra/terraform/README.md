# Hedvi AWS infrastructure

These modules describe an optional deployment in Stockholm (`eu-north-1`).
Use separate AWS accounts and Terraform state for staging and production.
The environment configurations have passed provider-schema validation.
End-to-end operation of this ECS stack still needs a staging deployment.

| Path | Resources |
|---|---|
| `bootstrap/` | Versioned account-specific state bucket with SSE-KMS, TLS-only access and public access blocked. Bootstrap itself uses local state. |
| `modules/hedvi/` | KMS, encrypted payload bucket, SQS queues and DLQs, SES configuration set and event delivery through SNS and SQS, runtime IAM policies, CloudWatch alarms and budget. |
| `modules/platform/` | VPC across two AZs, NAT, ALB and private access logs, ECR, ECS Fargate tasks, RDS PostgreSQL 18, authenticated TLS Valkey and Secrets Manager. |
| `envs/staging`, `envs/prod` | Environment roots with account-specific S3 backend and native lockfile locking. |

## Bootstrap and validate

Start in the Terraform directory. Use Terraform 1.11 or later. The AWS provider
is pinned to 6.73.0. After confirming the intended AWS account, initialize
`bootstrap/` and apply its reviewed plan with a distinct account alias.
Preserve its local state privately. Then work in the relevant environment root.

```bash
cp backend.hcl.example backend.hcl
# Set the returned bucket, allowed account ID and expected bucket owner.
terraform init -backend-config=backend.hcl
```

Set the environment's `api_domain`, `app_domain`, `site_domain`,
`certificate_arn` and immutable `image_tag`. Inject the ephemeral
`TF_VAR_valkey_auth_token` from a private secret store at both plan and apply.
For the first installation, explicitly set `api_count=0`, `worker_count=0`
and `web_count=0`. The deployment runbook covers image preparation with the
RDS CA, application secrets, migration and staged startup.
[Deployment runbook](../../docs/runbooks/deploy.md)

To validate the configuration without an AWS account, run from
`infra/terraform`. Repeat for `envs/prod`. Provider initialization requires a
local cache or access to the Terraform registry.

```bash
docker run --rm -v "$PWD":/tf -w /tf/envs/staging hashicorp/terraform:1.14 init -backend=false
docker run --rm -v "$PWD":/tf -w /tf/envs/staging hashicorp/terraform:1.14 validate
```

## Access and data handling

- Payloads use AES-256-GCM bound to the object key and SSE-KMS. The worker
  deletes expired content and sweeps abandoned objects. The unversioned bucket
  has a lifecycle expiration rule as a backstop.
- Each ECS service has its own execution role, secret allowlist and security
  group. The migration task alone receives the database owner URL. RDS accepts
  connections from API, worker and migration tasks. Valkey accepts API and worker
  connections using TLS and AUTH. Web tasks receive the proxy attestation key.
- The ALB routes the configured web hosts to port 3030 and the API host to 8780.
  It writes access logs to a separate private bucket with 30-day expiry.
  Production enables ALB and RDS deletion protection. RDS disables query and
  bind-parameter logging for ordinary execution and errors.
- The API manages claim-tagged SES identities. Identity creation requires
  a claim tag, and mutations require a claim-shaped existing tag. Applications
  additionally compare its exact value to the database claim UUID. Keep the
  installation in a dedicated SES account. The IAM tag-on-create permission can
  also add the first claim tag to an untagged identity.
- The worker sends using the configured SES configuration set. Configuration-set
  events go to the account-scoped SNS topic and SQS subscription. Hedvi records
  complaint and bounce suppressions per organization. Confirm the separate
  operations topic's subscription to receive worker alerts and CloudWatch alarms.

DNS zones and records, ACM certificates, SES production access and organization
alert recipients are operator inputs. WAF and VPC service endpoints are
additional deployment choices. The reference VPC uses NAT for outbound traffic.

Before initializing the S3 backend, verify the bucket belongs to your expected
account using `aws s3api head-bucket --bucket <state-bucket> --expected-bucket-owner <account-id>`.
Keep `allowed_account_ids` in the private backend configuration to check the caller
account as well. `expected_bucket_owner` is an AWS CLI option, not a Terraform S3
backend argument. See the [backend reference](https://developer.hashicorp.com/terraform/language/backend/s3).
