# Suppression scope

- **Scope: organization.** A permanent bounce or complaint suppresses the address for the whole organization (all its projects), never for other customers. One customer's complaint is not another customer's consent decision.
- **Hard bounce:** suppressed for 90 days, then eligible again (re-evaluated if it bounces again).
- **Complaint:** suppressed without expiry; each complaint opens an alert for review.
- **Manual/import:** via dashboard (audited) — CSV import is planned for the beta.
- **Removal:** owners/admins only, with note; kept as history (`removed_at`, `removed_by`).
- **SES account-level suppression is disabled** on Hedvi's configuration set (see Terraform), because it is shared across all tenants. SES tenant-level suppression may be enabled per tenant later; differences between Hedvi's list and SES lists must then be reconciled and documented.
- A future global abuse block (e.g. known spam traps) needs its own legal and operational basis and is not derived from customer complaints.
