# Contributing to Hedvi

Read README.md, SECURITY.md and the architecture documentation first. For security
issues use the private reporting address, not a public issue or pull request.

Discuss large changes before implementation. Keep changes focused, document public
API behavior, and add regression tests for new security or delivery invariants.
Use synthetic data and placeholders; never submit real messages, credentials,
Terraform state, customer identifiers or private deployment configuration.

Run `make test-go`, `make test-web`, `make contracts` and the relevant Terraform
validation before requesting review. Tests that skip their database do not count
as passing integration tests; use `HEDVI_REQUIRE_DB=1`. The local database uses
port 55432 and Valkey 56379 to avoid other projects.

## Rights and license

By contributing, you confirm that you have the right to submit your work under
this repository's AGPL-3.0-only license (and the additional MIT permission for
documentation integration snippets, where applicable). Keep third-party license
notices and disclose copied or generated material and its source. You retain
your copyright. Do not copy proprietary platform code or unlicensed examples.

Sign your commits with `git commit -s` to certify the Developer Certificate of
Origin: https://developercertificate.org/. Signing off is a statement of rights,
not a transfer of copyright.

Dependency versions are pinned and normally must have been public for at least
seven days. Security fixes may require a separately documented exception. Changes
to tenant isolation, quotas, permissions or payload handling need an integration
test in `apps/api/internal/e2e`.
