# Reporting a security vulnerability

Send reports privately to **hej@stackx.se** with the subject **Hedvi security**.
Include the affected commit/version, prerequisites, minimal reproduction and
impact. Do not put credentials, customer data or an unpatched exploit in a public
issue. Ask for a secure transfer channel before sending sensitive evidence.

We investigate reports, agree on reproduction where necessary, prepare a fix and
coordinate disclosure. There is no guaranteed response SLA or paid bounty. The
maintained development branch is `main`; older snapshots have no separate support
promise. A source release is not a certification or a guarantee of security.

## Safe testing

Use your own local instance and synthetic data. Permission to read the source does
not authorize testing StackX, AWS, Railway, other customers or other third parties.
Do not send unsolicited email, access another tenant, destroy data or exhaust
resources. If you encounter another person's information, stop and report it
privately without further access.

## Deployment boundary

Local demo credentials and simulator keys are intentionally public and are not
production credentials. Production requires separate least-privilege API, worker
and migration database roles, fresh secrets, HTTPS, verified sender ownership,
manual sending review, quotas and monitoring. Keep secrets, Terraform state,
backups and private account configuration outside the repository and image build
context. Never expose database, Valkey, worker or metrics ports publicly.

See [self-hosting](docs/self-hosting.md) for deployment responsibilities and
[architecture](docs/architecture/acceptance.md) for the trust model.
