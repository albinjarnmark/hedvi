// Package quota reserves, consumes and releases recipient quota atomically in
// PostgreSQL. Rules (docs/architecture/quota.md):
//
//   - One unit is one unique delivery recipient (To, Cc and Bcc deduplicated).
//   - Reservation happens in the acceptance transaction. Concurrent requests
//     serialize on the organization's period row, so they cannot exceed caps.
//   - Order: paid-cycle or baseline allowance, then Flex credits nearest expiry first (Flex
//     plans always, other plans only if the customer enabled it).
//     Insufficient allowance stops the send before acceptance.
//   - A reservation becomes consumed exactly once when SES accepts the
//     recipient. Rejected or cancelled unsent recipients are released exactly
//     once. Uncertain attempts stay reserved until reconciled.
//   - A job reserved in one period settles in that period; reserved credits
//     are honoured after the lot expires but never extend it.
//   - Test email never touches quota.
package quota

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrRecipientsExceeded = errors.New("recipient quota exceeded")
	ErrDataExceeded       = errors.New("attachment data budget exceeded")
)

type Org struct {
	ID                   uuid.UUID
	Plan                 string
	UseCreditsForOverage bool
	OverageEnabled       bool
	OverageCap           int64
}

type Period struct {
	ID                                           uuid.UUID
	Start, End                                   time.Time
	Included, Reserved, Consumed                 int64
	OverageCap, OverageReserved, OverageConsumed int64
	DataIncluded, DataReserved, DataConsumed     int64
	Warned80, Warned95                           bool
}

// EnsurePeriod creates the current monthly period if missing and locks it.
func EnsurePeriod(ctx context.Context, tx pgx.Tx, org Org, now time.Time) (Period, error) {
	return ensurePeriod(ctx, tx, org, now, true)
}

func ensurePeriod(ctx context.Context, tx pgx.Tx, org Org, now time.Time, lock bool) (Period, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT COALESCE(
        (SELECT id FROM usage_periods WHERE org_id=$1 AND (stripe_invoice IS NOT NULL OR complimentary_grant IS NOT NULL) AND billing_usable
          AND period_start<=$2 AND $2<period_end ORDER BY (stripe_invoice IS NOT NULL) DESC,period_start DESC LIMIT 1),
        hedvi_calendar_period($1,$2))`, org.ID, now).Scan(&id)
	if err != nil {
		return Period{}, err
	}
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE"
	}
	var per Period
	err = tx.QueryRow(ctx, `SELECT id, period_start, period_end, included, reserved, consumed,
			overage_cap, overage_reserved, overage_consumed, data_included, data_reserved, data_consumed,
			warned_80_at IS NOT NULL, warned_95_at IS NOT NULL
		FROM usage_periods WHERE org_id = $1 AND id = $2`+lockClause, org.ID, id).Scan(
		&per.ID, &per.Start, &per.End, &per.Included, &per.Reserved, &per.Consumed,
		&per.OverageCap, &per.OverageReserved, &per.OverageConsumed, &per.DataIncluded, &per.DataReserved, &per.DataConsumed,
		&per.Warned80, &per.Warned95)
	return per, err
}

type Allocation struct {
	ReservationID uuid.UUID
	Source        string
	Quantity      int
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// capacity returns how many units fit given remaining units and data.
func capacity(units, data, dataPerUnit int64) int64 {
	if units < 0 {
		units = 0
	}
	if dataPerUnit <= 0 {
		return units
	}
	if data < 0 {
		data = 0
	}
	return minInt64(units, data/dataPerUnit)
}

// Reserve allocates n units for an email. dataPerUnit is the attachment bytes
// sent per recipient. On error the caller must roll back the transaction.
func Reserve(ctx context.Context, tx pgx.Tx, org Org, emailID uuid.UUID, n int, dataPerUnit int64, now time.Time) ([]Allocation, Period, error) {
	if n <= 0 {
		return nil, Period{}, nil
	}
	per, err := EnsurePeriod(ctx, tx, org, now)
	if err != nil {
		return nil, per, err
	}

	remaining := int64(n)
	dataLimited := false
	var allocs []Allocation

	reserve := func(source string, periodID, lotID *uuid.UUID, qty int64) error {
		id := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO quota_reservations
			(id, org_id, email_id, source, usage_period_id, credit_lot_id, quantity, data_per_unit)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, org.ID, emailID, source, periodID, lotID, qty, dataPerUnit); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO credit_ledger
			(org_id, usage_period_id, credit_lot_id, reservation_id, email_id, kind, source, quantity, data_bytes)
			VALUES ($1, $2, $3, $4, $5, 'reserve', $6, $7, $8)`,
			org.ID, periodID, lotID, id, emailID, source, qty, qty*dataPerUnit); err != nil {
			return err
		}
		allocs = append(allocs, Allocation{ReservationID: id, Source: source, Quantity: int(qty)})
		remaining -= qty
		return nil
	}

	// 1. Monthly plan quota.
	unitsLeft := per.Included - per.Reserved - per.Consumed
	dataLeft := per.DataIncluded - per.DataReserved - per.DataConsumed
	if take := minInt64(remaining, capacity(unitsLeft, dataLeft, dataPerUnit)); take > 0 {
		if _, err := tx.Exec(ctx, `UPDATE usage_periods SET reserved = reserved + $2, data_reserved = data_reserved + $3 WHERE id = $1`,
			per.ID, take, take*dataPerUnit); err != nil {
			return nil, per, err
		}
		per.Reserved += take
		per.DataReserved += take * dataPerUnit
		if err := reserve("period", &per.ID, nil, take); err != nil {
			return nil, per, err
		}
	} else if remaining > 0 && unitsLeft > 0 {
		dataLimited = true
	}

	// 2. Flex credits, nearest expiry first.
	if remaining > 0 && (org.Plan == "flex" || org.UseCreditsForOverage) {
		rows, err := tx.Query(ctx, `SELECT id, granted - reserved - consumed - expired_amount, data_granted - data_reserved - data_consumed
			FROM credit_lots WHERE org_id = $1 AND billing_usable AND expires_at > $2 AND expired_at IS NULL
			ORDER BY expires_at, id FOR UPDATE`, org.ID, now)
		if err != nil {
			return nil, per, err
		}
		type lot struct {
			id          uuid.UUID
			units, data int64
		}
		var lots []lot
		for rows.Next() {
			var l lot
			if err := rows.Scan(&l.id, &l.units, &l.data); err != nil {
				rows.Close()
				return nil, per, err
			}
			lots = append(lots, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, per, err
		}
		for _, l := range lots {
			if remaining == 0 {
				break
			}
			take := minInt64(remaining, capacity(l.units, l.data, dataPerUnit))
			if take <= 0 {
				if l.units > 0 {
					dataLimited = true
				}
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE credit_lots SET reserved = reserved + $2, data_reserved = data_reserved + $3 WHERE id = $1`,
				l.id, take, take*dataPerUnit); err != nil {
				return nil, per, err
			}
			lotID := l.id
			if err := reserve("credit", nil, &lotID, take); err != nil {
				return nil, per, err
			}
		}
	}

	if remaining > 0 {
		if dataLimited {
			return nil, per, ErrDataExceeded
		}
		return nil, per, ErrRecipientsExceeded
	}
	return allocs, per, nil
}

type settled struct {
	reservationID uuid.UUID
	source        string
	periodID      *uuid.UUID
	lotID         *uuid.UUID
	n             int64
	dataPerUnit   int64
	emailID       uuid.UUID
}

// Consume marks reserved recipients as consumed. Recipients that are not in
// the reserved state are skipped, which makes the call idempotent.
func Consume(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, recipientIDs []uuid.UUID) (int64, error) {
	return settle(ctx, tx, orgID, recipientIDs, "consumed")
}

// Release returns reserved, unsent recipients to their source.
func Release(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, recipientIDs []uuid.UUID) (int64, error) {
	return settle(ctx, tx, orgID, recipientIDs, "released")
}

func settle(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, recipientIDs []uuid.UUID, to string) (int64, error) {
	if len(recipientIDs) == 0 {
		return 0, nil
	}
	column := map[string]string{"consumed": "consumed", "released": "released"}[to]
	rows, err := tx.Query(ctx, `
		WITH moved AS (
			UPDATE recipients SET billing_state = $3
			WHERE org_id = $1 AND id = ANY($2) AND billing_state = 'reserved'
			RETURNING reservation_id
		), counts AS (
			SELECT reservation_id, count(*) AS n FROM moved GROUP BY reservation_id
		)
		UPDATE quota_reservations r SET `+column+` = r.`+column+` + c.n,
			settled_at = CASE WHEN r.consumed + r.released + c.n = r.quantity THEN now() ELSE r.settled_at END
		FROM counts c WHERE r.id = c.reservation_id
		RETURNING r.id, r.source, r.usage_period_id, r.credit_lot_id, c.n, r.data_per_unit, r.email_id`,
		orgID, recipientIDs, to)
	if err != nil {
		return 0, err
	}
	var items []settled
	for rows.Next() {
		var s settled
		if err := rows.Scan(&s.reservationID, &s.source, &s.periodID, &s.lotID, &s.n, &s.dataPerUnit, &s.emailID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Consistent lock order across transactions avoids deadlocks.
	sort.Slice(items, func(i, j int) bool { return items[i].reservationID.String() < items[j].reservationID.String() })
	var total int64
	for _, s := range items {
		data := s.n * s.dataPerUnit
		var q string
		args := []any{s.n, data}
		switch {
		case s.source == "period" && to == "consumed":
			q = `UPDATE usage_periods SET reserved = reserved - $1, consumed = consumed + $1,
				data_reserved = data_reserved - $2, data_consumed = data_consumed + $2 WHERE id = $3`
			args = append(args, *s.periodID)
		case s.source == "period":
			q = `UPDATE usage_periods SET reserved = reserved - $1, data_reserved = data_reserved - $2 WHERE id = $3`
			args = append(args, *s.periodID)
		case s.source == "overage" && to == "consumed":
			q = `UPDATE usage_periods SET overage_reserved = overage_reserved - $1, overage_consumed = overage_consumed + $1 WHERE id = $2`
			args = []any{s.n, *s.periodID}
		case s.source == "overage":
			q = `UPDATE usage_periods SET overage_reserved = overage_reserved - $1 WHERE id = $2`
			args = []any{s.n, *s.periodID}
		case s.source == "credit" && to == "consumed":
			q = `UPDATE credit_lots SET reserved = reserved - $1, consumed = consumed + $1,
				data_reserved = data_reserved - $2, data_consumed = data_consumed + $2 WHERE id = $3`
			args = append(args, *s.lotID)
		default:
			// Released credits return to the lot unless it has expired, in
			// which case they expire too: a reservation never extends validity.
			q = `UPDATE credit_lots SET reserved = reserved - $1, data_reserved = data_reserved - $2,
				expired_amount = expired_amount + CASE WHEN expired_at IS NOT NULL THEN $1 ELSE 0 END WHERE id = $3`
			args = append(args, *s.lotID)
		}
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return 0, err
		}
		kind := map[string]string{"consumed": "consume", "released": "release"}[to]
		if _, err := tx.Exec(ctx, `INSERT INTO credit_ledger
			(org_id, usage_period_id, credit_lot_id, reservation_id, email_id, kind, source, quantity, data_bytes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			orgID, s.periodID, s.lotID, s.reservationID, s.emailID, kind, s.source, s.n, data); err != nil {
			return 0, err
		}
		total += s.n
	}
	return total, nil
}

// GrantCredits adds a Flex lot (purchase, grant or migration).
func GrantCredits(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, source, ref string, units, data int64, validity time.Duration, now time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `INSERT INTO credit_lots (org_id, source, source_ref, granted, data_granted, purchased_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, orgID, source, ref, units, data, now, now.Add(validity)).Scan(&id)
	if err != nil {
		return id, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO credit_ledger (org_id, credit_lot_id, kind, source, quantity, data_bytes, note)
		VALUES ($1, $2, 'grant', 'credit', $3, $4, $5)`, orgID, id, units, data, source+":"+ref)
	return id, err
}

// ExpireLots closes lots past expiry. Unreserved remainder expires now;
// reserved units settle later (consumed if sent, expired if released).
func ExpireLots(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error) {
	rows, err := tx.Query(ctx, `UPDATE credit_lots SET expired_at = $1,
			expired_amount = expired_amount + (granted - reserved - consumed - expired_amount)
		WHERE expires_at <= $1 AND expired_at IS NULL
		RETURNING id, org_id, expired_amount`, now)
	if err != nil {
		return 0, err
	}
	type exp struct {
		id, org uuid.UUID
		amount  int64
	}
	var list []exp
	for rows.Next() {
		var e exp
		if err := rows.Scan(&e.id, &e.org, &e.amount); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, e)
	}
	rows.Close()
	for _, e := range list {
		if _, err := tx.Exec(ctx, `INSERT INTO credit_ledger (org_id, credit_lot_id, kind, source, quantity, note)
			VALUES ($1, $2, 'expire', 'credit', $3, 'lot expired')`, e.org, e.id, e.amount); err != nil {
			return 0, err
		}
	}
	return int64(len(list)), rows.Err()
}

// Usage summarizes an organization's current position for API and dashboard.
type Usage struct {
	PeriodStart, PeriodEnd                       time.Time
	Included, Reserved, Consumed                 int64
	OverageCap, OverageReserved, OverageConsumed int64
	DataIncluded, DataReserved, DataConsumed     int64
	Credits                                      []CreditLot
}

type CreditLot struct {
	ID                                   uuid.UUID
	Granted, Reserved, Consumed, Expired int64
	ExpiresAt                            time.Time
	ExpiredAt                            *time.Time
	Available                            bool
}

func (u Usage) RemainingIncluded() int64 { return u.Included - u.Reserved - u.Consumed }

func LoadUsage(ctx context.Context, tx pgx.Tx, org Org, now time.Time) (Usage, error) {
	per, err := ensurePeriod(ctx, tx, org, now, false)
	if err != nil {
		return Usage{}, err
	}
	u := Usage{PeriodStart: per.Start, PeriodEnd: per.End, Included: per.Included, Reserved: per.Reserved, Consumed: per.Consumed,
		OverageCap: per.OverageCap, OverageReserved: per.OverageReserved, OverageConsumed: per.OverageConsumed,
		DataIncluded: per.DataIncluded, DataReserved: per.DataReserved, DataConsumed: per.DataConsumed}
	rows, err := tx.Query(ctx, `SELECT id, granted, reserved, consumed, expired_amount, expires_at, expired_at, billing_usable AND expires_at > $2 AND expired_at IS NULL
		FROM credit_lots WHERE org_id = $1 AND (expired_at IS NULL OR expired_at > $2::timestamptz - interval '30 days')
		ORDER BY expires_at`, org.ID, now)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var l CreditLot
		if err := rows.Scan(&l.ID, &l.Granted, &l.Reserved, &l.Consumed, &l.Expired, &l.ExpiresAt, &l.ExpiredAt, &l.Available); err != nil {
			return u, err
		}
		u.Credits = append(u.Credits, l)
	}
	return u, rows.Err()
}
