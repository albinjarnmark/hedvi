# Operator account mail infrastructure

This optional module addition creates a dedicated SES domain identity, default
configuration set and tenant named `operator-hedvi-<environment>-auth`. It shares
only the existing SES event topic/queue with customer mail. It does not publish
DNS, request production access, apply infrastructure, or change account data.
The sending mailbox is exactly `accounts@<auth_mail_domain>`.

Set `auth_mail_domain = "auth.example.com"` in the private environment inputs.
`auth_mail_from` may be omitted or explicitly set to `accounts@auth.example.com`.
The domain must be a dedicated, operator-controlled subdomain; it and its parents
must not be assigned to a customer project. The name is outside customer tenant
prefix `hedvi-*`. Existing standalone auth resources must be imported deliberately
before apply; do not create duplicate identities or steal existing customer ones.
Empty domain keeps this infrastructure disabled. Hosted auth still needs all
three `HEDVI_AUTH_MAIL_*` values from a separately provisioned operator sender.

The module exports `auth_mail_dns_records`: three Easy DKIM CNAMEs (RSA-2048),
`bounce.<domain>` MX and SPF, and `_dmarc.<domain>` with reject policy. Use the
returned full names with DNS proxying disabled. SES rejects mail if the custom
MAIL FROM MX is unavailable; transport TLS is required. DKIM/MX status must be
verified after DNS publication before enabling registrations. The dedicated
identity's default configuration set covers omitted `ConfigurationSetName`;
explicit requests for another set do not receive an operator send grant.

`env` exports `HEDVI_AUTH_MAIL_FROM`, `HEDVI_AUTH_MAIL_TENANT` and
`HEDVI_AUTH_MAIL_CONFIGURATION_SET`. The platform module also sets
`HEDVI_LEGAL_ORIGIN=https://<site_domain>` alongside the application origin.
The auth worker always supplies its fixed mailbox, operator tenant and set.

Both runtime policies explicitly deny mutation/tagging/association of the auth
identity (including ancestors, subordinate identities and email identities),
operator tenant and auth configuration set. The public API receives no send
allow. The worker's auth allow binds the exact From address and tenant, and only
allows the dedicated identity/configuration-set resources. Its customer allow
excludes the auth mailbox case-insensitively even if a verified parent identity
would otherwise authorize it; a shared deny also rejects that mailbox outside
the operator tenant. Customer API code additionally reserves the full DNS
namespace. Runtime credentials are not the Terraform operator credentials.

## Pinned provider limitation and suppression behavior

The installed AWS provider **6.73.0** schema was inspected using
`terraform providers schema -json`. `aws_sesv2_configuration_set` supports
`suppression_options.suppressed_reasons`, but has no `suppression_scope` field;
`aws_sesv2_tenant` also has no suppression attributes. This module therefore sets
both `BOUNCE` and `COMPLAINT` on the auth configuration set, using the account
suppression list. The customer configuration set retains empty reasons, so those
customer sends do not check or record into this list. Other independently
configured senders in the AWS account can still affect the account list. Do not
claim tenant-scoped suppression from this Terraform configuration. Do not add
unsupported HCL attributes or an unreviewed local-exec provisioner to imply it.

Provider suppression entries do not automatically follow Hedvi's 90-day
application bounce TTL. They can continue to block until a reviewed operator
removal. Keep privacy copy and the support process explicit about this boundary.
The auth worker retains pseudonymous authenticated event correlation for 90 days;
provider suppression remains a backstop for later feedback.

## Validation and operator release checks

The module was validated with Terraform 1.14 / AWS 6.73.0. Tests in
`modules/hedvi/tests/auth_mail.tftest.hcl` run **plan only**, use synthetic account
IDs and override infrastructure values, and render the real IAM namespace guard.
They cover disabled/enabled behavior, both runtime guards, absence of API send,
worker mailbox/tenant/resource binding, customer exclusion, TLS/suppression and
invalid inputs. Run with container networking disabled after provider init:

```sh
terraform -chdir=infra/terraform/modules/hedvi validate
terraform -chdir=infra/terraform/modules/hedvi test
```

These source tests are not an AWS effective-permission certification. The
operator must inspect the complete policies attached to deployed API/worker
principals, check permissions boundaries/SCPs, and verify the following with
IAM simulation and safe SES simulator recipients before public registration:

| Principal / request | Expected |
| --- | --- |
| API sends any auth mail | Denied |
| API or worker creates/tags/deletes/modifies/associates auth identity, parent identity, operator tenant or set | Denied |
| Worker: exact auth From + operator tenant + auth set + auth domain identity | Allowed |
| Worker: auth From + customer tenant, including mixed-case From | Denied |
| Worker: auth From + no tenant | Denied |
| Worker: auth From + operator tenant + customer set | Denied |
| Worker: auth From + operator tenant + verified parent identity ARN | Denied |
| Worker: another From + operator tenant + auth set | Denied |
| Worker: customer From + own project tenant + customer set | Allowed |
| Worker: exact auth From + operator tenant, omitted set | Auth default set applies |

After operator apply, verify DKIM and MAIL FROM; tenant identity/set associations;
configuration-set default, TLS and both suppression reasons; SES→SNS→SQS event
receipt; generic signup→mail→single-use completion; reset revokes every session
and preserves MFA; and late bounce/complaint handling. Never expose real tokens
or passwords in logs or test evidence. Migration 0009 belongs to the auth code
patch and must be run with API/worker drained; it revokes legacy pilot keys and
sessions. This infrastructure patch never runs it.

## References

- [AWS SES v2 IAM actions, resources and condition keys](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sesv2.html): `ses:FromAddress` and `ses:TenantName` are supported. There is no `ses:ConfigurationSetName` condition; constrain the configuration-set resource instead.
- [SES default configuration sets](https://docs.aws.amazon.com/ses/latest/dg/managing-configuration-sets.html) and [tenant configuration-set requirement](https://docs.aws.amazon.com/ses/latest/dg/tenants.html).
- [SES suppression precedence](https://docs.aws.amazon.com/ses/latest/dg/sending-email-suppression-list-tenant-level.html) and [configuration-set suppression](https://docs.aws.amazon.com/ses/latest/dg/sending-email-suppression-list-config-level.html).
- [Pinned AWS provider source](https://github.com/hashicorp/terraform-provider-aws/tree/v6.73.0/internal/service/sesv2). Schema inspection is the authority for which HCL fields this patch can use.

## Customer environment isolation

Customer tenant names use `HEDVI_SES_TENANT_PREFIX` plus the existing 32-character
project UUID representation. Staging keeps `hedvi-`; production uses
`hedvi-production-`. Deployed configuration rejects other prefixes. The module
emits the matching value in its `env` output. Operator tenants remain separate.

New customer SES identities and tenants carry `environment=staging|production`.
Create requests require this tag; customer sending, identity mutation and tenant,
identity or configuration-set association require the matching existing tag.
An explicit deny protects resources tagged for the other environment. Staging
also explicitly denies production tenant/configuration namespaces because its
legacy `hedvi-*` allow would otherwise match `hedvi-production-*`. Both API and
worker inherit these guards. Read-before-create can observe NotFound for its own
tenant namespace; the provider code verifies existing names and environment tags
before using them. The runtime cannot change an already tagged foreign resource
into its own environment or remove environment tags.

Before upgrading the existing pilot, the operator must inventory and backfill
`environment=staging` on the exact known customer tenant and identity after
checking their database claim/project mapping. Terraform config sets already
carry the environment tag. Do not automatically tag unknown resources or publish
customer identifiers. Untagged legacy resources intentionally fail ownership
checks; the new runtime is not an operator migration tool. Backfill all managed
legacy SES resources before relying on the tag boundary. Deploy matching Go and
IAM changes together and test with both principals. Never simply rename an
existing SES tenant; staging's prefix remains stable and production starts with
its distinct prefix before customer resources are allocated.
