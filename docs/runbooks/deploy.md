# Deploy the optional AWS stack

Staging and production use separate AWS accounts in Stockholm. This procedure has
been validated against the Terraform provider schema. A live ECS deployment,
including its IAM, network and TLS paths, still needs a staging rehearsal.
The **Build AWS images** workflow publishes images to ECR. The operator runs the
schema migration and controls service counts.

## First installation

1. Confirm the account with `aws sts get-caller-identity`. Obtain an ACM
   certificate in `eu-north-1` covering the chosen site, dashboard and API DNS
   names. Use the staging account first.
2. In `infra/terraform/bootstrap`, initialize Terraform and apply a reviewed
   plan with the intended account alias. Keep the local bootstrap state private.
   In `infra/terraform/envs/staging`, copy `backend.hcl.example` to the ignored
   `backend.hcl`, set the bucket returned by bootstrap, the permitted account
   and the expected bucket owner, then run
   `terraform init -backend-config=backend.hcl`.
3. Use Terraform 1.11 or later. Supply `api_domain`, `app_domain`, `site_domain`,
   `certificate_arn` and an immutable release image tag. Generate a random
   cache token, for example with `openssl rand -hex 32`, in a private secret
   store and inject it as `TF_VAR_valkey_auth_token` during both plan and apply.
   Terraform passes the ephemeral value to write-only AWS provider fields.
   Keep state, saved plans and CI logs private. Set `api_count`, `worker_count`
   and `web_count` to zero for the first infrastructure apply. Review and apply
   the plan, then use `terraform output -json platform` for the resource names.
4. Push ARM64 images under the release tag to the API and web repositories.
   PostgreSQL connections require the current regional RDS roots to be readable
   by the non-root user in the API, worker and migration containers. Obtain the
   Stockholm PEM from the AWS RDS trust store, verify its provenance, record the
   reviewed digest and bake or mount it read-only at
   `/etc/hedvi/certs/eu-north-1-bundle.pem`. A derived API image can copy the
   public PEM there in its final stage. Publish that image under the intended
   immutable tag. Coordinate CA bundle updates with RDS CA rotations.
   Build the web container from the repository root with `-f apps/web/Dockerfile`.
   Set the build arguments `HEDVI_PUBLIC_API_URL`,
   `NEXT_PUBLIC_HEDVI_SUPPORT_EMAIL` and `NEXT_PUBLIC_HEDVI_SOURCE_URL` to
   the operator's public API, support contact and deployed source revision.
   `HEDVI_API_INTERNAL_URL` is set by Terraform at runtime.
   [AWS regional certificate bundles](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.SSL.html)
5. Use the RDS-managed master secret from a private operator connection to
   bootstrap the dedicated logins as described in `database-bootstrap.md`.
   Terraform already creates the `hedvi` database. Use
   `ALTER DATABASE hedvi OWNER TO hedvi_owner` for that existing database in
   place of the runbook's `CREATE DATABASE` command, using a database
   administrator with permission to assign that owner. Generate separate login
   passwords. Store URL-encoded credentials and the actual RDS endpoint in
   the three application Secrets Manager entries. For example, use
   `postgres://<role>:<encoded-password>@<rds-endpoint>:5432/hedvi?sslmode=verify-full&sslrootcert=/etc/hedvi/certs/eu-north-1-bundle.pem`.
6. Populate the remaining application secrets. Generate separate base64-encoded
   32-byte secrets for `HEDVI_DATA_KEY`, `HEDVI_API_KEY_PEPPER` and
   `HEDVI_PROXY_ATTESTATION_KEY`, for example with separate
   `openssl rand -base64 32` calls. Keep a recoverable backup of the data
   encryption key. The ECS execution roles read these assignments.

   | Task | Secrets Manager entries |
   |---|---|
   | API | API database URL, data key, API key pepper, authenticated Valkey URL, proxy attestation key |
   | Worker | Worker database URL, data key, API key pepper, authenticated Valkey URL |
   | Migration | Owner database URL, data key, API key pepper for configuration validation |
   | Web | Proxy attestation key |

   Terraform provisions the authenticated Valkey URL. Operational alerts use the
   encrypted SNS topic by default. Confirm its email subscription. To select a
   private operations webhook for worker-generated alerts, populate the
   `HEDVI_OPS_ALERT_WEBHOOK_URL` secret **before** setting
   `enable_ops_webhook=true`; CloudWatch alarms continue to publish to SNS.
7. Point the three DNS names at the ALB. The web and API security groups accept
   traffic only from the ALB at their respective target ports. The web proxy
   uses the rightmost address appended by this ALB and signs its console
   forwarding headers. The API trusts the designated ALB subnet CIDRs.
   Use the direct ALB ingress arrangement for these settings. A CDN or another
   upstream proxy needs its own explicitly verified client-IP configuration.
8. Run the migration and staged rollout below.

## Schema changes and upgrades

1. Take and verify a recoverable database backup, record previous image digests
   and desired counts, then pause public mutations. For upgrades across the
   idempotency, domain ownership, acceptance budget and worker safety migrations,
   stop the old API and worker before migrating. Set their desired counts to
   zero and wait for the tasks to stop. Keep public writes paused through domain
   verification. Retain the prior task and service configuration for recovery.
2. With API and worker counts held at zero, apply a reviewed plan that registers
   the **new release** migration task and updates its dependencies. Use the
   `cluster`, `migration_task_definition`, `private_subnets` and
   `migration_security_group` from the environment's `platform` output. Run
   that task with Fargate, those private subnets, its dedicated security group
   and public IP disabled. Inspect the `failures` array from `aws ecs run-task`.
   Wait for the returned task ARN with `aws ecs wait tasks-stopped`, then run
   `aws ecs describe-tasks`. Require an empty failures list, `STOPPED` and a
   recorded exit code of **0** for the `migrate` container. Inspect its private
   logs and keep services paused on failure.
3. Start the new API and web with **worker_count still zero**. Check ECS stability
   and `/readyz`. Readiness pings the database. API startup separately checks
   database-role privileges and tenant RLS. Exercise signup, test-mode capture
   and scoped API authorization.
4. For old active sending domains, an operator must first reconcile any legacy
   SES identity's claim tag against independently verified account, domain and
   database ownership. Use the current claim's dashboard TXT record and refresh
   verification. Migration 0005 resets prior verification. Before resuming the
   sender, confirm that domains used by queued messages are verified under the
   new code. For a **new** domain, publish the claim-specific TXT within the
   72-hour challenge window, have an organization owner/admin accept the current
   documents, then click Verify to provision SES. Publish the displayed DKIM and MAIL FROM records and refresh
   until verified. The worker performs its own DNS/provider check and activates
   unrestricted new projects automatically at 25/hour and 100/day per project.
   Paused projects and restricted organizations never auto-reactivate. Keep the ownership TXT published.
5. Restore the intended worker count. Use the isolated worker task role and
   database credential for operator commands. For example, a one-off worker
   task can run `admin approve-project -project <id> -note "<review basis>"`.
   Send controlled test messages to SES mailbox simulator addresses and verify
   the resulting delivery, bounce, complaint, suppression, idempotency replay
   and webhook behavior before reopening writes.
6. Appoint the single installation owner after independent identity verification
   and enrollment of both the ordinary and separate owner authenticators.
   Follow the [private worker and owner bootstrap procedure](hosted-owner.md).

Schema migrations run forward. Reusing a previous image requires checking its
compatibility with the migrated schema. A restore needs a reviewed recovery
plan covering already-accepted messages and uncertain provider outcomes.

## Valkey credential rotation

The module creates a TLS cache with required AUTH. For an existing cache with no
token, or to replace an active token, provide the new secret to both Terraform
plan and apply, increase `valkey_auth_token_version`, and temporarily set
`valkey_auth_token_update_strategy=ROTATE`. After the new Secrets Manager URL
version is available, confirm the new API and worker tasks using it are healthy.
For an originally unauthenticated cache, old unauthenticated connections remain
possible during this stage. Apply again with the **same token and version** and
`valkey_auth_token_update_strategy=SET`. Confirm the current token succeeds and
the old credentials or unauthenticated connection fail. Use the private operator
environment for these checks.
[AWS ElastiCache AUTH rotation](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/auth.html)
