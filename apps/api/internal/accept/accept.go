// Package accept implements durable acceptance of send requests.
//
// An email is accepted only when one PostgreSQL transaction has committed the
// email row, its recipients, the quota reservation, the outbox job and the
// idempotency record. The encrypted payload is uploaded before that
// transaction; if the commit fails the object is an orphan that the sweeper
// removes. No worker can send an object without an accepted row.
package accept

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/message"
	"hedvi.dev/api/internal/plans"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/secure"
	"hedvi.dev/api/internal/webhooks"
)

// Principal is an authenticated API key.
type Principal struct {
	KeyID      uuid.UUID
	OrgID      uuid.UUID
	ProjectID  uuid.UUID
	Mode       string // "test" or "live", read from the key row
	Permission string
	DomainID   *uuid.UUID
}

type Service struct {
	ReservedSenderDomain string
	Pool                 db.Beginner
	Blobs                blob.Store
	Limits               message.Limits
	Faults               *faults.Injector
	MaxQueueTime         time.Duration
	Now                  func() time.Time
	Log                  *slog.Logger
	// LoopThreshold is how many emails to the same recipient within
	// LoopWindow are treated as a probable mail loop.
	LoopThreshold int
	LoopWindow    time.Duration
}

type Result struct {
	Status   int
	Body     json.RawMessage
	EmailID  uuid.UUID
	Replayed bool
}

const OpSendEmail = "POST /emails"

const IdempotencyTTL = 24 * time.Hour

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func ValidIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > 256 {
		return false
	}
	for _, r := range k {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// Live acceptance windows count every unique requested recipient, including
// suppressions. The storage budget counts each retained canonical payload once.
// Neither measure is a commercial quota reservation.
type liveBudget struct {
	hourlyLimit, dailyLimit int
	hourUsed, dayUsed       int
	storageRemaining        int64
}

func (b liveBudget) check(recipients int, payloadBytes int64) error {
	if payloadBytes > b.storageRemaining {
		return apierr.New(429, "live_storage_limit_exceeded", "This project has reached its live email storage limit. Space becomes available when content retention removes payloads. Contact support for a reviewed limit increase.")
	}
	if recipients > b.hourlyLimit-b.hourUsed {
		return liveWindowError("live_hour", b.hourlyLimit)
	}
	if recipients > b.dailyLimit-b.dayUsed {
		return liveWindowError("live_day", b.dailyLimit)
	}
	return nil
}

func liveWindowError(kind string, limit int) error {
	if kind == "live_hour" {
		return apierr.New(429, "rate_limit_exceeded", "This project's hourly acceptance limit is %d unique recipients, including suppressed recipients.", limit)
	}
	return apierr.New(429, "daily_quota_exceeded", "This project's daily acceptance limit is %d unique recipients, including suppressed recipients. It resets at 00:00 UTC.", limit)
}

type orgContext struct {
	plan                string
	riskStatus          string
	metadataDays        *int32
	contentDays         *int32
	useCredits, overage bool
	overageCap          int64
	sendingStatus       string
	liveBudget          liveBudget
	testDailyUsed       int
	testDailyLimit      int
	testStorageLimit    int64
	domainID            *uuid.UUID
	domainStatus        string
	suppressed          map[string]bool
	testStorageUsed     int64
	loopSuspects        []string
}

func (s *Service) loadContext(ctx context.Context, tx pgx.Tx, p Principal, m *message.Message, now time.Time, committing bool, oc *orgContext) error {
	err := tx.QueryRow(ctx, `SELECT hedvi_effective_plan(o.id,$4), o.risk_status, o.metadata_retention_days, o.content_retention_days,
				o.use_credits_for_overage, o.overage_enabled, o.overage_cap_recipients,
				p.sending_status, p.hourly_limit, p.daily_limit, p.test_daily_limit, p.test_storage_limit_bytes,
				coalesce((SELECT count FROM send_counters WHERE project_id=p.id AND window_kind='test_day' AND window_start=$3),0),
                p.live_storage_limit_bytes - p.live_stored_payload_bytes,
				coalesce((SELECT count FROM send_counters WHERE project_id = p.id AND window_kind = 'live_hour' AND window_start = $2), 0),
				coalesce((SELECT count FROM send_counters WHERE project_id = p.id AND window_kind = 'live_day' AND window_start = $3), 0)
			FROM projects p JOIN organizations o ON o.id = p.org_id WHERE p.id = $1`, p.ProjectID, now.UTC().Truncate(time.Hour), truncDay(now), now).Scan(
		&oc.plan, &oc.riskStatus, &oc.metadataDays, &oc.contentDays, &oc.useCredits, &oc.overage, &oc.overageCap,
		&oc.sendingStatus, &oc.liveBudget.hourlyLimit, &oc.liveBudget.dailyLimit, &oc.testDailyLimit, &oc.testStorageLimit, &oc.testDailyUsed,
		&oc.liveBudget.storageRemaining, &oc.liveBudget.hourUsed, &oc.liveBudget.dayUsed)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	if p.Mode == "test" {
		return tx.QueryRow(ctx, `SELECT coalesce(sum(payload_bytes), 0) FROM emails
				WHERE project_id = $1 AND mode = 'test' AND content_deleted_at IS NULL`, p.ProjectID).Scan(&oc.testStorageUsed)
	}
	var id uuid.UUID
	dq := `SELECT id,status FROM domains WHERE project_id=$1 AND name=$2 AND deleted_at IS NULL`
	if committing {
		dq += " FOR SHARE"
	}
	err = tx.QueryRow(ctx, dq,
		p.ProjectID, m.FromAddr.Domain).Scan(&id, &oc.domainStatus)
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	if err == nil {
		oc.domainID = &id
	}
	normalized := make([]string, len(m.Recipients))
	for i, r := range m.Recipients {
		normalized[i] = r.Normalized
	}
	rows, err := tx.Query(ctx, `SELECT normalized FROM suppressions WHERE org_id = $1 AND normalized = ANY($2)
			AND removed_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, p.OrgID, normalized)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		oc.suppressed[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if s.LoopThreshold > 0 {
		rows, err = tx.Query(ctx, `SELECT r.normalized FROM recipients r JOIN emails e ON e.id = r.email_id
				WHERE r.org_id = $1 AND r.normalized = ANY($2) AND e.mode = 'live' AND e.created_at > $3
				GROUP BY r.normalized HAVING count(*) >= $4`, p.OrgID, normalized, s.now().Add(-s.LoopWindow), s.LoopThreshold)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			oc.loopSuspects = append(oc.loopSuspects, n)
		}
		return rows.Err()
	}
	return nil
}

// Send validates and durably accepts one email.
func (s *Service) Send(ctx context.Context, p Principal, idemKey string, body []byte) (*Result, error) {
	return s.send(ctx, p, OpSendEmail, idemKey, body, nil)
}

// Resend records the source link and console audit event in the acceptance
// transaction. The original email gets its own retry namespace.
func (s *Service) Resend(ctx context.Context, p Principal, source uuid.UUID, idemKey string, body []byte, audit func(pgx.Tx, uuid.UUID) error) (*Result, error) {
	return s.send(ctx, p, "resend/"+source.String(), idemKey, body, func(tx pgx.Tx, id uuid.UUID) error {
		if _, err := tx.Exec(ctx, `UPDATE emails SET resend_of=$2 WHERE id=$1`, id, source); err != nil {
			return err
		}
		return audit(tx, id)
	})
}

func (s *Service) send(ctx context.Context, p Principal, operation, idemKey string, body []byte, afterInsert func(pgx.Tx, uuid.UUID) error) (*Result, error) {
	if idemKey != "" && !ValidIdempotencyKey(idemKey) {
		return nil, apierr.New(400, "invalid_idempotency_key", "The Idempotency-Key header must be 1-256 printable ASCII characters.")
	}
	req, err := message.Decode(body)
	if err != nil {
		return nil, validation(err)
	}
	m, err := req.Validate(s.Limits)
	if err != nil {
		return nil, validation(err)
	}
	if s.ReservedSenderDomain != "" && (m.FromAddr.Domain == s.ReservedSenderDomain || strings.HasSuffix(m.FromAddr.Domain, "."+s.ReservedSenderDomain)) {
		return nil, apierr.New(403, "reserved_sender", "This sender domain is reserved for account security mail.")
	}
	canonical, err := m.Canonical()
	if err != nil {
		return nil, err
	}
	hash := secure.SHA256(canonical)

	if idemKey != "" {
		if res, err := s.replay(ctx, p, operation, idemKey, hash); res != nil || err != nil {
			return res, err
		}
	}

	now := s.now()
	err = db.InOrg(ctx, s.Pool, p.OrgID, func(tx pgx.Tx) error {
		oc := &orgContext{suppressed: map[string]bool{}}
		if err := s.loadContext(ctx, tx, p, m, now, false, oc); err != nil {
			return err
		}
		if err := s.checkPolicy(p, m, oc, int64(len(canonical))); err != nil {
			return err
		}
		if p.Mode == "test" {
			return nil
		}
		return quotaError(quota.CheckCapacity(ctx, tx, oc.quotaOrg(p.OrgID), len(m.Recipients)-len(oc.suppressed), m.AttachmentBytes(), now))
	})
	if err != nil {
		if idemKey != "" {
			if result, replayErr := s.replay(ctx, p, operation, idemKey, hash); result != nil || replayErr != nil {
				return result, replayErr
			}
		}
		return nil, err
	}

	emailID := uuid.Must(uuid.NewV7())
	key := blob.PayloadKey(p.OrgID, emailID)
	if err := s.Blobs.Put(ctx, key, canonical); err != nil {
		return nil, fmt.Errorf("store payload: %w", err)
	}
	if err := s.Faults.Check(faults.AcceptAfterBlobPut); err != nil {
		return nil, err
	}
	respBody, _ := json.Marshal(map[string]string{"id": emailID.String()})

	readyToCommit := false
	err = db.InOrg(ctx, s.Pool, p.OrgID, func(tx pgx.Tx) error {
		if err := s.insert(ctx, tx, p, m, emailID, key, hash, int64(len(canonical)), idemKey, now); err != nil {
			return err
		}
		if afterInsert != nil {
			if err := afterInsert(tx, emailID); err != nil {
				return err
			}
		}
		if idemKey != "" {
			// Remove only expired records in this namespace under the same transaction.
			if _, err := tx.Exec(ctx, `DELETE FROM idempotency_records WHERE org_id=$1 AND project_id=$2 AND operation=$3 AND key=$4 AND mode IN ($5,'legacy') AND expires_at <= now()`, p.OrgID, p.ProjectID, operation, idemKey, p.Mode); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO idempotency_records
				(org_id, project_id, operation, key, request_sha256, response_status, response_body, resource_id, expires_at, mode)
				VALUES ($1, $2, $3, $4, $5, 200, $6, $7, $8, $9)`,
				p.OrgID, p.ProjectID, operation, idemKey, hash, respBody, emailID, now.Add(IdempotencyTTL), p.Mode); err != nil {
				return err
			}
		}
		if err := s.Faults.Check(faults.AcceptBeforeCommit); err != nil {
			return err
		}
		readyToCommit = true
		return nil
	})
	if err != nil {
		if errors.Is(err, faults.ErrCrash) {
			return nil, err // a crash leaves the orphan for the sweeper
		}
		// An error receiving COMMIT's response can arrive after PostgreSQL
		// made the row durable. Only an acceptance that never reached COMMIT
		// can safely discard its blob here. The orphan sweep resolves the rest.
		if !readyToCommit {
			if derr := s.Blobs.Delete(context.WithoutCancel(ctx), key); derr != nil && s.Log != nil {
				s.Log.Warn("delete payload after failed acceptance", "err", derr)
			}
		}
		if idemKey != "" {
			// A concurrent copy may have consumed the last quota before this
			// transaction reached the idempotency insert. Return its saved result.
			res, rerr := s.replay(ctx, p, operation, idemKey, hash)
			if res != nil || rerr != nil {
				return res, rerr
			}
		}
		return nil, err
	}
	if err := s.Faults.Check(faults.AcceptAfterCommit); err != nil {
		return nil, err
	}
	return &Result{Status: http.StatusOK, Body: respBody, EmailID: emailID}, nil
}

func (oc *orgContext) quotaOrg(id uuid.UUID) quota.Org {
	return quota.Org{ID: id, Plan: oc.plan, UseCreditsForOverage: oc.useCredits, OverageEnabled: oc.overage, OverageCap: oc.overageCap}
}

func testDayError(limit int) error {
	return apierr.New(429, "daily_quota_exceeded", "Daily test email limit reached (%d per project). It resets at 00:00 UTC.", limit)
}

func quotaError(err error) error {
	switch {
	case errors.Is(err, quota.ErrDataExceeded):
		return apierr.New(429, "monthly_quota_exceeded",
			"The attachment data budget for this period is used up. Nothing was sent or charged. Send without attachments, add Flex credits or upgrade.")
	case errors.Is(err, quota.ErrRecipientsExceeded):
		return apierr.New(429, "monthly_quota_exceeded",
			"Your recipient quota is used up and hard stop is on: nothing was sent or charged. Add prepaid Flex credits. Note that this also stops sign-in and password emails.")
	default:
		return err
	}
}

func validation(err error) error {
	var ve *message.ValidationError
	if errors.As(err, &ve) {
		return &apierr.Error{Status: 422, Name: ve.Name, Message: ve.Message}
	}
	return err
}

func (s *Service) replay(ctx context.Context, p Principal, operation, key string, hash []byte) (*Result, error) {
	var stored []byte
	var status int
	var body []byte
	var resource *uuid.UUID
	var mode string
	var allowed bool
	err := db.InOrg(ctx, s.Pool, p.OrgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT request_sha256, response_status, response_body, resource_id, mode,
 ($6::uuid IS NULL OR EXISTS (SELECT 1 FROM emails e WHERE e.id = resource_id AND e.domain_id = $6))
 FROM idempotency_records WHERE org_id = $1 AND project_id = $2 AND operation = $3 AND key = $4 AND mode IN ($5, 'legacy') AND expires_at > now()`,
			p.OrgID, p.ProjectID, operation, key, p.Mode, p.DomainID).Scan(&stored, &status, &body, &resource, &mode, &allowed)
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mode == "legacy" || !allowed {
		return nil, apierr.New(409, "invalid_idempotent_request", "This Idempotency-Key is unavailable for this key. Use a new key for a new operation.")
	}
	if string(stored) != string(hash) {
		return nil, apierr.New(409, "invalid_idempotent_request",
			"This Idempotency-Key was already used with a different request body. Use a new key for a new email.")
	}
	res := &Result{Status: status, Body: body, Replayed: true}
	if resource != nil {
		res.EmailID = *resource
	}
	return res, nil
}

func (s *Service) checkPolicy(p Principal, m *message.Message, oc *orgContext, payloadBytes int64) error {
	if oc.riskStatus == "paused" || oc.riskStatus == "blocked" {
		return apierr.New(403, "security_error", "Sending is paused for this organization. Contact your service operator.")
	}
	if p.Mode == "test" {
		if oc.testDailyUsed >= oc.testDailyLimit {
			return testDayError(oc.testDailyLimit)
		}
		if payloadBytes > oc.testStorageLimit-oc.testStorageUsed {
			return apierr.New(422, "validation_error",
				"The test inbox is full (%d MB). Delete test emails in the dashboard or wait for the 7-day cleanup.", oc.testStorageLimit/plans.MB)
		}
		return nil
	}
	switch oc.sendingStatus {
	case "approved":
	case "paused":
		return apierr.New(403, "security_error", "Live sending is paused for this project. See the dashboard for the reason, or contact your service operator.")
	default:
		return apierr.New(403, "validation_error",
			"Live sending is not enabled for this project yet. Request access under Settings. Test keys work right away.")
	}
	if oc.domainID == nil {
		return apierr.New(403, "validation_error",
			"The %s domain is not added to this project. Add and verify it under Domains.", m.FromAddr.Domain)
	}
	if oc.domainStatus != "verified" {
		return apierr.New(403, "validation_error",
			"The %s domain is not verified yet. Check the DNS records under Domains.", m.FromAddr.Domain)
	}
	if p.DomainID != nil && *p.DomainID != *oc.domainID {
		return apierr.New(403, "invalid_from_address", "This API key may only send from its assigned domain.")
	}
	if len(oc.loopSuspects) > 0 {
		return apierr.New(429, "rate_limit_exceeded",
			"Possible mail loop: %s received too many emails from this organization in the last %s. Contact support if this is expected.",
			oc.loopSuspects[0], s.LoopWindow)
	}
	return oc.liveBudget.check(len(m.Recipients), payloadBytes)
}

func (s *Service) insert(ctx context.Context, tx pgx.Tx, p Principal, m *message.Message,
	emailID uuid.UUID, key string, hash []byte, payloadBytes int64, idemKey string, now time.Time) error {
	// Lock in parent-first order. Loop detection spans every project in the
	// organization, so serialize those live decisions across projects.
	lockOrg := " FOR SHARE"
	if p.Mode == "live" && s.LoopThreshold > 0 {
		lockOrg = " FOR NO KEY UPDATE"
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1`+lockOrg, p.OrgID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, p.ProjectID); err != nil {
		return err
	}
	now = s.now()
	oc := &orgContext{suppressed: map[string]bool{}}
	if err := s.loadContext(ctx, tx, p, m, now, true, oc); err != nil {
		return err
	}
	if err := s.checkPolicy(p, m, oc, payloadBytes); err != nil {
		return err
	}
	plan, err := plans.Get(oc.plan)
	if err != nil {
		return err
	}
	metadata, content := plans.Retention(plan, oc.metadataDays, oc.contentDays)
	mode, sendState, lastEvent := p.Mode, "queued", "queued"
	if mode == "test" {
		metadata, content = plans.TestRetention, plans.TestRetention
		sendState, lastEvent = "captured", "test_captured"
	}

	var billable []int
	for i, r := range m.Recipients {
		if mode == "live" && !oc.suppressed[r.Normalized] {
			billable = append(billable, i)
		}
	}
	if mode == "live" && len(billable) == 0 {
		sendState, lastEvent = "skipped", "suppressed"
	}

	if mode == "test" {
		if err := bumpCounter(ctx, tx, p, "test_day", truncDay(now), 1, oc.testDailyLimit); err != nil {
			if errors.Is(err, errLimit) {
				return testDayError(oc.testDailyLimit)
			}
			return err
		}
	} else {
		budget := oc.liveBudget
		n := len(m.Recipients)
		if err := bumpCounter(ctx, tx, p, "live_hour", now.UTC().Truncate(time.Hour), n, budget.hourlyLimit); err != nil {
			if errors.Is(err, errLimit) {
				return liveWindowError("live_hour", budget.hourlyLimit)
			}
			return err
		}
		if err := bumpCounter(ctx, tx, p, "live_day", truncDay(now), n, budget.dailyLimit); err != nil {
			if errors.Is(err, errLimit) {
				return liveWindowError("live_day", budget.dailyLimit)
			}
			return err
		}
	}

	tags, _ := json.Marshal(m.Tags)
	if m.Tags == nil {
		tags = []byte("[]")
	}
	var domainID *uuid.UUID
	if mode == "live" {
		domainID = oc.domainID
	}
	var idem *string
	if idemKey != "" {
		idem = &idemKey
	}
	var keyID *uuid.UUID
	if p.KeyID != uuid.Nil {
		keyID = &p.KeyID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO emails (id, org_id, project_id, api_key_id, domain_id, mode, from_address, from_domain,
			subject, to_addresses, cc_addresses, bcc_addresses, reply_to, tags, payload_ref, payload_sha256, payload_bytes,
			attachment_count, attachment_bytes, idempotency_key, last_event, last_event_at, send_state, next_attempt_at,
			created_at, queue_deadline_at, content_expires_at, metadata_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$22,$22,$24,$25,$26)`,
		emailID, p.OrgID, p.ProjectID, keyID, domainID, mode, m.From, m.FromAddr.Domain,
		m.Subject, nonNil(m.To), nonNil(m.Cc), nonNil(m.Bcc), nonNil(m.ReplyTo), tags, key, hash, payloadBytes,
		len(m.Attachments), m.AttachmentBytes(), idem, lastEvent, now, sendState,
		now.Add(s.MaxQueueTime), now.Add(content), now.Add(metadata)); err != nil {
		return fmt.Errorf("insert email: %w", err)
	}

	// Reserve quota for billable recipients and assign reservations in order.
	reservationOf := make(map[int]uuid.UUID, len(billable))
	if len(billable) > 0 {
		org := oc.quotaOrg(p.OrgID)
		allocs, period, err := quota.Reserve(ctx, tx, org, emailID, len(billable), m.AttachmentBytes(), now)
		if err != nil {
			return quotaError(err)
		}
		i := 0
		for _, a := range allocs {
			for range a.Quantity {
				reservationOf[billable[i]] = a.ReservationID
				i++
			}
		}
		if err := s.quotaWarnings(ctx, tx, p.OrgID, period); err != nil {
			return err
		}
	}

	for i, r := range m.Recipients {
		status, billing := "queued", "none"
		var res *uuid.UUID
		switch {
		case mode == "test":
			status = "test_captured"
		case oc.suppressed[r.Normalized]:
			status = "suppressed"
		default:
			billing = "reserved"
			id := reservationOf[i]
			res = &id
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recipients (org_id, email_id, address, normalized, field, status, status_at, billing_state, reservation_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.OrgID, emailID, r.Email, r.Normalized, string(r.Field), status, now, billing, res); err != nil {
			return fmt.Errorf("insert recipient: %w", err)
		}
		if status == "suppressed" {
			if err := webhooks.Emit(ctx, tx, webhooks.Event{OrgID: p.OrgID, ProjectID: p.ProjectID, Type: webhooks.EmailSuppressed,
				EmailID: &emailID, CreatedAt: now, Data: EventData(emailID, now, m.From, []string{r.Email}, m.Subject, m.Tags,
					map[string]any{"suppressed": map[string]string{"message": "The recipient is on this organization's suppression list.", "type": "OnOrganizationSuppressionList"}})}); err != nil {
				return err
			}
		}
	}

	if sendState == "queued" {
		msg, _ := json.Marshal(map[string]string{"email_id": emailID.String()})
		if _, err := tx.Exec(ctx, `INSERT INTO outbox (org_id, topic, message, available_at) VALUES ($1, 'send', $2, $3)`, p.OrgID, msg, now); err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}
	}
	return nil
}

// EventData builds the Resend-compatible webhook data object.
func EventData(emailID uuid.UUID, createdAt time.Time, from string, to []string, subject string, tags []message.Tag, extra map[string]any) map[string]any {
	t := map[string]string{}
	for _, tag := range tags {
		t[tag.Name] = tag.Value
	}
	d := map[string]any{
		"email_id":   emailID.String(),
		"created_at": createdAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"from":       from,
		"to":         to,
		"subject":    subject,
		"tags":       t,
	}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func (s *Service) quotaWarnings(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, per quota.Period) error {
	if per.Included == 0 {
		return nil
	}
	used := per.Reserved + per.Consumed
	for _, th := range []struct {
		pct    int64
		column string
		done   bool
	}{{80, "warned_80_at", per.Warned80}, {95, "warned_95_at", per.Warned95}} {
		if th.done || used*100 < per.Included*th.pct {
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE usage_periods SET `+th.column+` = now() WHERE id = $1 AND `+th.column+` IS NULL`, per.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		msg := fmt.Sprintf("%d%% of this month's %d included recipients are used. At 100%% sending stops (hard stop), including sign-in and password emails.", th.pct, per.Included)
		if _, err := tx.Exec(ctx, `INSERT INTO alerts (org_id, kind, severity, dedupe_key, message, data)
			VALUES ($1, 'quota_threshold', 'warning', $2, $3, $4) ON CONFLICT DO NOTHING`,
			orgID, fmt.Sprintf("quota:%s:%d:%s", orgID, th.pct, per.Start.Format("2006-01")), msg,
			map[string]any{"percent": th.pct, "included": per.Included, "used": used}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM projects WHERE org_id = $1`, orgID)
		if err != nil {
			return err
		}
		var projects []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			projects = append(projects, id)
		}
		rows.Close()
		for _, pid := range projects {
			if err := webhooks.Emit(ctx, tx, webhooks.Event{OrgID: orgID, ProjectID: pid, Type: webhooks.QuotaWarning,
				Data: map[string]any{"percent": th.pct, "included": per.Included, "used": used, "message": msg,
					"period_start": per.Start.Format(time.RFC3339), "period_end": per.End.Format(time.RFC3339)}}); err != nil {
				return err
			}
		}
	}
	return nil
}

var errLimit = errors.New("limit reached")

func bumpCounter(ctx context.Context, tx pgx.Tx, p Principal, kind string, window time.Time, n, limit int) error {
	if n > limit {
		return errLimit
	}
	var count int
	err := tx.QueryRow(ctx, `INSERT INTO send_counters (org_id, project_id, window_kind, window_start, count)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, window_kind, window_start)
		DO UPDATE SET count = send_counters.count + EXCLUDED.count WHERE send_counters.count <= $6 - EXCLUDED.count
		RETURNING count`, p.OrgID, p.ProjectID, kind, window, n, limit).Scan(&count)
	if db.IsNoRows(err) {
		return errLimit
	}
	return err
}

func truncDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// MaskAddress hides most of an address where the full value is not needed.
func MaskAddress(a string) string {
	at := strings.LastIndexByte(a, '@')
	if at <= 1 {
		return "***" + a[max(at, 0):]
	}
	return a[:1] + "***" + a[at:]
}
