package domains

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/legal"
	"hedvi.dev/api/internal/plans"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/webhooks"
)

type Domain struct {
	ProviderState    string
	ID               uuid.UUID
	OrgID            uuid.UUID
	ProjectID        uuid.UUID
	Name             string
	Region           string
	Status           string
	MailFrom         string
	MailFromRevision int64
	DKIMTokens       []string
	Records          []Record
	Analysis         Analysis
	CheckAttempts    int
	CheckStarted     time.Time
	LastError        string
	CreatedAt        time.Time
	LastCheckedAt    *time.Time
	NextCheckAt      *time.Time
	VerifiedAt       *time.Time
}

type Service struct {
	TenantEnv      config.Env
	ReservedDomain string
	Pool           *pgxpool.Pool
	Provider       provider.Provider
	Resolver       Resolver
	MockDNS        bool
	MailFromMX     string
	Region         string
	Log            *slog.Logger
}

// DomainColumns is shared by API and worker queries decoded by ScanDomain.
const DomainColumns = `id, org_id, project_id, name, region, status, mail_from_subdomain, dkim_tokens, records, analysis,
 check_attempts, check_started_at, last_error, created_at, last_checked_at, next_check_at, verified_at, provider_state, mail_from_revision`
const selectDomain = "SELECT " + DomainColumns + " FROM domains"

func ScanDomain(row pgx.Row) (*Domain, error) {
	var d Domain
	var records, analysis []byte
	if err := row.Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.Name, &d.Region, &d.Status, &d.MailFrom, &d.DKIMTokens, &records, &analysis,
		&d.CheckAttempts, &d.CheckStarted, &d.LastError, &d.CreatedAt, &d.LastCheckedAt, &d.NextCheckAt, &d.VerifiedAt, &d.ProviderState, &d.MailFromRevision); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(records, &d.Records)
	d.Records = withOwnershipRecord(d.Records, d.Name, d.ID)
	_ = json.Unmarshal(analysis, &d.Analysis)
	if d.Analysis.Findings == nil {
		d.Analysis.Findings = []Finding{}
	}
	return &d, nil
}

func (s *Service) Create(ctx context.Context, orgID, projectID uuid.UUID, rawName, mailFrom string) (*Domain, error) {
	name, err := NormalizeName(rawName)
	if err != nil {
		return nil, apierr.New(422, "validation_error", "%s", err)
	}
	if s.reservedAuth(name) {
		return nil, apierr.New(403, "reserved_domain", "This domain is reserved for account security mail.")
	}
	if mailFrom == "" {
		mailFrom = "send"
	}
	if !ValidMailFromLabel(mailFrom) || len(mailFrom)+1+len(name) > 253 {
		return nil, apierr.New(422, "validation_error", "`custom_return_path` must be a single DNS label such as \"send\".")
	}
	if !s.MockDNS && reservedName(name) {
		return nil, apierr.New(422, "validation_error", "Reserved domain names only work in local development.")
	}
	var d *Domain
	// Serialize the organization’s claim limit. Unproven projects can challenge the same name independently.
	err = db.InOrg(ctx, s.Pool, orgID, func(tx pgx.Tx) error {
		var planID string
		if err := tx.QueryRow(ctx, `SELECT hedvi_effective_plan(id,statement_timestamp()) FROM organizations WHERE id=$1 FOR UPDATE`, orgID).Scan(&planID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE domains SET deleted_at=now(),next_check_at=NULL
          WHERE org_id=$1 AND deleted_at IS NULL AND provider_state='awaiting_dns' AND created_at <= now()-interval '72 hours'`, orgID); err != nil {
			return err
		}
		existing, err := ScanDomain(tx.QueryRow(ctx, selectDomain+` WHERE project_id=$1 AND name=$2 AND deleted_at IS NULL`, projectID, name))
		if err == nil {
			if (existing.ProviderState == "awaiting_dns" || existing.ProviderState == "provisioning") && existing.MailFrom == mailFrom {
				d = existing
				return nil
			}
			return claimConflict()
		}
		if !db.IsNoRows(err) {
			return err
		}
		plan, err := plans.Get(planID)
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM domains WHERE org_id=$1 AND deleted_at IS NULL`, orgID).Scan(&n); err != nil {
			return err
		}
		if n >= plan.MaxDomains {
			return apierr.New(403, "validation_error", "Your plan allows %d domains. Remove one or upgrade.", plan.MaxDomains)
		}
		d, err = ScanDomain(tx.QueryRow(ctx, `INSERT INTO domains (org_id,project_id,name,region,mail_from_subdomain,status,provider_state,next_check_at)
   VALUES ($1,$2,$3,$4,$5,'pending','awaiting_dns',now()+interval '72 hours') RETURNING `+DomainColumns, orgID, projectID, name, s.Region, mailFrom))
		return err
	})
	if db.IsUniqueViolation(err, "domains_active_name_idx") {
		return nil, claimConflict()
	}
	if err != nil {
		return nil, err
	}
	if s.MockDNS && strings.HasSuffix(name, ".test") {
		return s.check(ctx, s.Pool, d, true)
	}
	return d, nil
}

func approvalRequired() error {
	return apierr.New(409, "validation_error", "Accept the current terms to continue domain setup. Contact support if sending has been paused.")
}

func (s *Service) liveConsent(ctx context.Context, tx pgx.Tx, org uuid.UUID) (bool, error) {
	m := legal.Current()
	if (s.TenantEnv == config.EnvStaging || s.TenantEnv == config.EnvProduction) && !m.Adopted {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT hedvi_org_has_live_consent($1,$2,$3,$4)`, org, m.Version, m.Documents, m.Adopted).Scan(&ok)
	return ok, err
}

func (s *Service) projectApproved(ctx context.Context, tx pgx.Tx, project uuid.UUID) (bool, error) {
	var tenant, state, risk string
	var org uuid.UUID
	err := tx.QueryRow(ctx, `SELECT p.ses_tenant_name,p.sending_status,o.risk_status,o.id FROM projects p
        JOIN organizations o ON o.id=p.org_id WHERE p.id=$1 FOR SHARE OF o,p`, project).Scan(&tenant, &state, &risk, &org)
	if err != nil {
		return false, err
	}
	if err := s.TenantEnv.CheckProjectTenant(project, tenant); err != nil {
		return false, err
	}
	if risk != "new" && risk != "approved" {
		return false, nil
	}
	if state == "approved" {
		return true, nil
	}
	if state != "test_only" && state != "pending_review" {
		return false, nil
	}
	var restricted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE org_id=$1 AND sending_status='paused')`, org).Scan(&restricted); err != nil {
		return false, err
	}
	if restricted {
		return false, nil
	}
	return s.liveConsent(ctx, tx, org)
}

// Only a worker can commit live access, after its own fresh DNS and SES reads.
// Keep the org -> project lock order used by operator decisions and never
// reset a pause, a reviewed limit, or the risk observation window.
func (s *Service) activateVerified(ctx context.Context, tx pgx.Tx, d *Domain) error {
	var risk, state, tenant string
	if err := tx.QueryRow(ctx, `SELECT risk_status FROM organizations WHERE id=$1 FOR UPDATE`, d.OrgID).Scan(&risk); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT sending_status,ses_tenant_name FROM projects WHERE id=$1 AND org_id=$2 FOR UPDATE`, d.ProjectID, d.OrgID).Scan(&state, &tenant); err != nil {
		return err
	}
	if risk != "new" && risk != "approved" || state != "test_only" && state != "pending_review" {
		return nil
	}
	if err := s.TenantEnv.CheckProjectTenant(d.ProjectID, tenant); err != nil {
		return err
	}
	var restricted, worker bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE org_id=$1 AND sending_status='paused'),pg_has_role(current_user,'hedvi_worker','USAGE')`, d.OrgID).Scan(&restricted, &worker); err != nil {
		return err
	}
	if restricted || !worker {
		return nil
	}
	ok, err := s.liveConsent(ctx, tx, d.OrgID)
	if err != nil || !ok {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE projects SET sending_status='approved',status_reason='Domain verified; live sending enabled automatically',status_changed_at=now() WHERE id=$1`, d.ProjectID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE organizations SET risk_status='approved' WHERE id=$1 AND risk_status='new'`, d.OrgID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE dedupe_key=$1 AND resolved_at IS NULL`, "review:"+d.ProjectID.String()); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(org_id,actor_kind,action,target_type,target_id,metadata)
		SELECT $1,'system','project.live_auto_activated','project',$2::uuid::text,
		jsonb_build_object('domain_id',$3::uuid,'legal_version',$4::text,'hourly',l.hourly,'daily',l.daily)
		FROM hedvi_live_limits($2::uuid,now()) l`, d.OrgID, d.ProjectID, d.ID, legal.Current().Version)
	return err
}

func claimConflict() error {
	return apierr.New(409, "validation_error", "This domain already has a claim. Contact support to resolve ownership.")
}

func (s *Service) Get(ctx context.Context, orgID, projectID, id uuid.UUID) (*Domain, error) {
	var d *Domain
	err := db.InOrg(ctx, s.Pool, orgID, func(tx pgx.Tx) error {
		var err error
		d, err = ScanDomain(tx.QueryRow(ctx, selectDomain+` WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`, id, projectID))
		return err
	})
	if db.IsNoRows(err) {
		return nil, apierr.ErrNotFound
	}
	return d, err
}

func (s *Service) List(ctx context.Context, orgID, projectID uuid.UUID) ([]*Domain, error) {
	var out []*Domain
	err := db.InOrg(ctx, s.Pool, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectDomain+` WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := ScanDomain(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// Verify retries an interrupted provisioning operation or refreshes DNS and SES.
func (s *Service) Verify(ctx context.Context, orgID, projectID, id uuid.UUID) (*Domain, error) {
	d, err := s.Get(ctx, orgID, projectID, id)
	if err != nil {
		return nil, err
	}
	return s.check(ctx, s.Pool, d, true)
}

// Only SES create/delete need a cross-process mutex. A session advisory lock
// survives short independent commits with no open transaction during AWS I/O.
// Use the acquired connection for every DB step and bound total pinned sessions.
var providerSlots = make(chan struct{}, 2)
var checkSlots = make(chan struct{}, 6)
var checking sync.Map // claim IDs currently checked by this process

func withProviderLock(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, f func(*pgxpool.Conn) error) error {
	select {
	case providerSlots <- struct{}{}:
		defer func() { <-providerSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if keep {
			conn.Release()
			return
		}
		// A lost lock/unlock response must never return a locked connection to the pool.
		c := conn.Hijack()
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Close(cleanup)
	}()
	var locked bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 43369))`, id.String()).Scan(&locked)
	if err != nil {
		return err
	}
	if !locked {
		keep = true
		return apierr.New(409, "validation_error", "Another domain operation is in progress. Retry shortly.")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var unlocked bool
		err := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1, 43369))`, id.String()).Scan(&unlocked)
		keep = err == nil && unlocked
	}()
	return f(conn)
}

func (s *Service) Delete(ctx context.Context, orgID, projectID, id uuid.UUID) error {
	d, err := s.Get(ctx, orgID, projectID, id)
	if err != nil {
		return err
	}
	if s.reservedAuth(d.Name) {
		return apierr.New(403, "reserved_domain", "This domain is reserved for account security mail.")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var done bool
	err = db.InOrg(ctx, s.Pool, orgID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE domains SET
          deleted_at=CASE WHEN provider_state='awaiting_dns' THEN now() END,
          provider_state=CASE WHEN provider_state='awaiting_dns' THEN provider_state ELSE 'deleting' END,
          status='pending', verified_at=NULL,next_check_at=NULL
          WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL RETURNING deleted_at IS NOT NULL`, id, projectID).Scan(&done)
		if db.IsNoRows(err) {
			return apierr.ErrNotFound
		}
		if err == nil && done {
			return resolveProviderAlert(ctx, tx, id)
		}
		return err
	})
	if err != nil || done {
		return err
	}
	return withProviderLock(ctx, s.Pool, id, func(conn *pgxpool.Conn) error {
		var name string
		err := db.InOrg(ctx, conn, orgID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT name FROM domains WHERE id=$1 AND project_id=$2 AND provider_state='deleting' AND deleted_at IS NULL`, id, projectID).Scan(&name)
		})
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = s.Provider.DeleteIdentity(ctx, name, id); err != nil {
			return ownershipError(err)
		}
		return db.InOrg(ctx, conn, orgID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE domains SET deleted_at=now() WHERE id=$1 AND provider_state='deleting' AND deleted_at IS NULL`, id)
			if err != nil {
				return err
			}
			return resolveProviderAlert(ctx, tx, id)
		})
	})
}

func ownershipError(err error) error {
	if errors.Is(err, provider.ErrIdentityOwnership) {
		return apierr.New(409, "validation_error", "The provider identity belongs to another claim. Contact support to confirm ownership.")
	}
	return err
}

func (s *Service) resolver(d *Domain) Resolver {
	if s.MockDNS {
		return MockResolver{Expected: d.Records, Fallback: s.Resolver}
	}
	return s.Resolver
}

// CheckNow is used by the worker and only reads provider resources. The API
// resumes provider provisioning after ownership proof through Verify.
func (s *Service) CheckNow(ctx context.Context, pool *pgxpool.Pool, d *Domain) (*Domain, error) {
	return s.check(ctx, pool, d, false)
}

func (s *Service) check(ctx context.Context, pool *pgxpool.Pool, requested *Domain, manual bool) (*Domain, error) {
	if s.reservedAuth(requested.Name) {
		return nil, apierr.New(403, "reserved_domain", "This domain is reserved for account security mail.")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, busy := checking.LoadOrStore(requested.ID, struct{}{}); busy {
		return nil, apierr.New(409, "validation_error", "Domain verification is already in progress.")
	}
	defer checking.Delete(requested.ID)
	select {
	case checkSlots <- struct{}{}:
		defer func() { <-checkSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var d *Domain
	err := db.InOrg(ctx, pool, requested.OrgID, func(tx pgx.Tx) error {
		var err error
		d, err = ScanDomain(tx.QueryRow(ctx, selectDomain+` WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL`, requested.ID, requested.ProjectID))
		return err
	})
	if db.IsNoRows(err) {
		return nil, apierr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if d.ProviderState == "deleting" {
		if manual {
			return nil, apierr.New(409, "validation_error", "The identity is being removed. Retry deletion or contact support.")
		}
		return d, nil
	}
	if d.ProviderState == "awaiting_dns" && time.Since(d.CreatedAt) >= 72*time.Hour {
		err := db.InOrg(ctx, pool, d.OrgID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE domains SET deleted_at=now(),next_check_at=NULL WHERE id=$1 AND provider_state='awaiting_dns' AND created_at<=now()-interval '72 hours'`, d.ID)
			return err
		})
		if err != nil {
			return nil, err
		}
		return nil, apierr.ErrNotFound
	}
	if d.ProviderState == "awaiting_dns" || d.ProviderState == "provisioning" {
		if !manual {
			return d, nil
		}
		record := OwnershipRecord(d.Name, d.ID)
		got, err := s.resolver(d).LookupTXT(ctx, record.FQDN+".")
		proof := err == nil && slices.Contains(got, record.Value)
		record.Status = statusFor(err, proof)
		var ready bool
		err = db.InOrg(ctx, pool, d.OrgID, func(tx pgx.Tx) error {
			approved, err := s.projectApproved(ctx, tx, d.ProjectID)
			if err != nil {
				return err
			}
			if !proof || !approved {
				analysis := Analysis{CheckedAt: time.Now().UTC(), Findings: []Finding{}}
				if proof && !approved {
					analysis.Findings = append(analysis.Findings, Finding{"info", "live_access_required", "DNS ownership confirmed. An organization owner or admin must accept the current terms. If sending is paused, contact support."})
				}
				state := d.ProviderState
				d, err = ScanDomain(tx.QueryRow(ctx, `UPDATE domains SET records=$2,analysis=$3,status='pending',last_checked_at=now()
                    WHERE id=$1 AND deleted_at IS NULL AND provider_state=$4 AND mail_from_revision=$5 RETURNING `+DomainColumns, d.ID, []Record{record}, analysis, state, d.MailFromRevision))
				if db.IsNoRows(err) {
					return claimConflict()
				}
				return err
			}
			// Commit exclusive custody before the first write to SES. A new
			// project cannot consume a global reservation without ownership and consent.
			if d.ProviderState == "awaiting_dns" {
				tag, err := tx.Exec(ctx, `UPDATE domains SET provider_state='provisioning',next_check_at=NULL
                    WHERE id=$1 AND deleted_at IS NULL AND provider_state='awaiting_dns' AND created_at>now()-interval '72 hours'`, d.ID)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return claimConflict()
				}
			}
			ready = true
			return nil
		})
		if db.IsUniqueViolation(err, "domains_active_name_idx") {
			return nil, claimConflict()
		}
		if err != nil {
			return nil, err
		}
		if !ready {
			return d, nil
		}
		err = withProviderLock(ctx, pool, d.ID, func(conn *pgxpool.Conn) (operationErr error) {
			var attempt *Domain
			// Record failures before releasing the provider lock, and only if
			// the same provisioning generation still owns this attempt.
			defer func() {
				var known *apierr.Error
				if operationErr != nil && attempt != nil && !errors.As(operationErr, &known) {
					s.provisioningAlert(ctx, conn, attempt, operationErr)
				}
			}()
			var tenant string
			err := db.InOrg(ctx, conn, d.OrgID, func(tx pgx.Tx) error {
				allowed, err := s.projectApproved(ctx, tx, d.ProjectID)
				if err != nil {
					return err
				}
				if !allowed {
					return approvalRequired()
				}
				attempt, err = ScanDomain(tx.QueryRow(ctx, selectDomain+` WHERE id=$1 AND project_id=$2 AND provider_state='provisioning' AND deleted_at IS NULL`, d.ID, d.ProjectID))
				if err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT p.ses_tenant_name FROM projects p JOIN domains d ON d.project_id=p.id WHERE d.id=$1 AND d.provider_state='provisioning' AND d.deleted_at IS NULL`, d.ID).Scan(&tenant)
			})
			if db.IsNoRows(err) {
				return claimConflict()
			}
			if err != nil {
				return err
			}
			if err = s.TenantEnv.CheckProjectTenant(d.ProjectID, tenant); err != nil {
				return err
			}
			if err = s.Provider.EnsureTenant(ctx, tenant); err != nil {
				return err
			}
			if err := db.InOrg(ctx, conn, d.OrgID, func(tx pgx.Tx) error {
				approved, err := s.projectApproved(ctx, tx, d.ProjectID)
				if err != nil {
					return err
				}
				if !approved {
					return approvalRequired()
				}
				return nil
			}); err != nil {
				return err
			}
			// The bounce label can change before this operation acquires the
			// provider lock. Use the generation read under that lock.
			ident, err := s.Provider.CreateIdentity(ctx, attempt.Name, attempt.MailFrom+"."+attempt.Name, tenant, attempt.ID)
			if err != nil {
				return ownershipError(err)
			}
			return db.InOrg(ctx, conn, d.OrgID, func(tx pgx.Tx) error {
				approved, err := s.projectApproved(ctx, tx, d.ProjectID)
				if err != nil {
					return err
				}
				if !approved {
					return approvalRequired()
				}
				d, err = ScanDomain(tx.QueryRow(ctx, `UPDATE domains SET provider_state='active',dkim_tokens=$2,provider_identity=name,next_check_at=now() WHERE id=$1 AND provider_state='provisioning' AND deleted_at IS NULL RETURNING `+DomainColumns, d.ID, ident.DKIMTokens))
				if db.IsNoRows(err) {
					return claimConflict()
				}
				return err
			})
		})
		if err != nil {
			return nil, err
		}
	} else if manual && d.LastCheckedAt != nil && time.Since(*d.LastCheckedAt) < 10*time.Second {
		return d, nil
	}
	originalState := d.ProviderState
	// Build the required records from this claim on every verification. A
	// stored DNS result or a reused SES selector cannot replace fresh proof.
	d.Records = ExpectedRecords(d.Name, d.MailFrom, s.MailFromMX, d.DKIMTokens, d.ID)
	if manual && d.Status == "failed" {
		d.CheckAttempts = 0
		d.CheckStarted = time.Now()
	}
	previous := d.Status
	analysis, _ := Check(ctx, s.resolver(d), d.Name, d.Records)
	st, providerErr := s.Provider.GetIdentity(ctx, d.Name, d.ID)
	lastErr := ""
	status := "temporary_failure"
	if providerErr != nil {
		if s.Log != nil {
			s.Log.Error("domain provider unavailable", "domain_id", d.ID, "err", providerErr)
		}
		lastErr = "Could not confirm the provider identity. Sending is disabled until it can be verified."
		if errors.Is(providerErr, provider.ErrIdentityOwnership) {
			lastErr = "Provider identity ownership needs operator review."
		}
		analysis.Findings = append(analysis.Findings, Finding{"warning", "provider_check_failed", lastErr})
	} else {
		d.ProviderState = "active"
		var verified *bool
		if !st.FollowDNS {
			mailFromReady := st.MailFromStatus == "SUCCESS" && strings.EqualFold(st.MailFromDomain, d.MailFrom+"."+d.Name)
			v := st.VerifiedForSending && st.DKIMStatus == "SUCCESS" && mailFromReady
			verified = &v
			if !mailFromReady {
				analysis.Findings = append(analysis.Findings, Finding{"info", "mail_from_pending", "Amazon SES has not confirmed the current MAIL FROM domain yet."})
			}
		}
		status = Overall(d.Records, verified)
	}
	now := time.Now()
	next, keepPolling := NextCheck(d.CheckStarted, d.CheckAttempts, now)
	var nextPtr *time.Time
	switch {
	case status == "verified":
		next = now.Add(24 * time.Hour)
		nextPtr = &next
	case keepPolling:
		nextPtr = &next
	default:
		status = "failed"
	}
	recordsJSON, _ := json.Marshal(d.Records)
	analysisJSON, _ := json.Marshal(analysis)
	var out *Domain
	err = db.InOrg(ctx, pool, d.OrgID, func(tx pgx.Tx) error {
		if status == "verified" {
			if !manual && providerErr == nil && (st.FollowDNS || st.MailFromStatus == "SUCCESS") {
				if err := s.activateVerified(ctx, tx, d); err != nil {
					return err
				}
			}
			var candidate bool
			if err := tx.QueryRow(ctx, `SELECT sending_status IN ('test_only','pending_review') FROM projects WHERE id=$1`, d.ProjectID).Scan(&candidate); err != nil {
				return err
			}
			if candidate {
				eligible, err := s.projectApproved(ctx, tx, d.ProjectID)
				if err != nil {
					return err
				}
				if eligible {
					if manual {
						soon := now
						nextPtr = &soon
					} else if soon, ok := NextCheck(d.CheckStarted, d.CheckAttempts, now); ok {
						nextPtr = &soon
					}
				}
			}
		}
		out, err = ScanDomain(tx.QueryRow(ctx, `UPDATE domains SET status=$2,records=$3,analysis=$4,ns_provider=$5,
   check_attempts=$6,check_started_at=$7,last_checked_at=$8,next_check_at=$9,verified_at=CASE WHEN $2='verified' THEN coalesce(verified_at,$8) ELSE NULL END,
   provider_state=$10,dkim_tokens=$11,provider_identity=name,last_error=$12,
   mail_from_reconfiguring=mail_from_reconfiguring AND $2<>'verified'
   WHERE id=$1 AND provider_state=$13 AND deleted_at IS NULL AND last_checked_at IS NOT DISTINCT FROM $14
   AND org_id=$15 AND project_id=$16 AND name=$17 AND mail_from_subdomain=$18 AND dkim_tokens=$11 AND mail_from_revision=$19 RETURNING `+DomainColumns, d.ID, status, recordsJSON, analysisJSON, analysis.NSProvider, d.CheckAttempts+1, d.CheckStarted, now, nextPtr, d.ProviderState, d.DKIMTokens, lastErr, originalState, d.LastCheckedAt, d.OrgID, d.ProjectID, d.Name, d.MailFrom, d.MailFromRevision))
		if db.IsNoRows(err) {
			return apierr.New(409, "validation_error", "Domain changed during verification. Refresh its status.")
		}
		if err != nil {
			return err
		}
		// A stale provider response must not open or resolve an incident.
		// The domain CAS above holds its row lock until this alert commits.
		if providerErr != nil {
			if err := raiseProviderAlert(ctx, tx, out); err != nil {
				return err
			}
		} else if err := resolveProviderAlert(ctx, tx, d.ID); err != nil {
			return err
		}
		if previous != status {
			return webhooks.Emit(ctx, tx, webhooks.Event{OrgID: d.OrgID, ProjectID: d.ProjectID, Type: webhooks.DomainUpdated, Data: map[string]any{
				"id": d.ID.String(), "name": d.Name, "status": status, "previous_status": previous, "created_at": d.CreatedAt.UTC().Format(time.RFC3339), "region": d.Region}})
		}
		return nil
	})
	return out, err
}

// Ancestors must not capture the reserved SES namespace either.
func (s *Service) reservedAuth(name string) bool {
	return s.ReservedDomain != "" && (name == s.ReservedDomain || strings.HasSuffix(name, "."+s.ReservedDomain) || strings.HasSuffix(s.ReservedDomain, "."+name))
}

// Provisioning has no final domain CAS on failure. Its caller holds the
// provider advisory lock; also lock and check the current domain generation so
// a concurrent delete or newer DNS result cannot leave a stale incident.
func (s *Service) provisioningAlert(ctx context.Context, conn *pgxpool.Conn, d *Domain, cause error) {
	if s.Log != nil {
		s.Log.Error("domain provider unavailable", "domain_id", d.ID, "err", cause)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	err := db.InOrg(ctx, conn, d.OrgID, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM domains WHERE id=$1 AND project_id=$2
			AND provider_state='provisioning' AND deleted_at IS NULL
			AND last_checked_at IS NOT DISTINCT FROM $3 AND name=$4 AND mail_from_subdomain=$5 AND dkim_tokens=$6
			FOR UPDATE`, d.ID, d.ProjectID, d.LastCheckedAt, d.Name, d.MailFrom, d.DKIMTokens).Scan(&id)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return raiseProviderAlert(ctx, tx, d)
	})
	if err != nil && s.Log != nil {
		s.Log.Error("persist domain provider alert", "domain_id", d.ID, "err", err)
	}
}

func raiseProviderAlert(ctx context.Context, tx pgx.Tx, d *Domain) error {
	_, err := tx.Exec(ctx, `INSERT INTO alerts(org_id,project_id,kind,severity,dedupe_key,message,data)
		VALUES($1,$2,'domain_provider_unavailable','critical',$3,'Domain setup could not reach the email provider. Check runtime permissions and provider health.',$4)
		ON CONFLICT DO NOTHING`, d.OrgID, d.ProjectID, "domain-provider:"+d.ID.String(), map[string]string{"domain_id": d.ID.String()})
	return err
}

func resolveProviderAlert(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE dedupe_key=$1 AND resolved_at IS NULL`, "domain-provider:"+id.String())
	return err
}
