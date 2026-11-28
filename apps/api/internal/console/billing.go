package console

import (
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/billing"
	"net/http"
)

func (s *Server) billingStatus(w http.ResponseWriter, r *http.Request, a *access) {
	if s.Billing == nil {
		writeJSON(w, 200, billing.Status{Enabled: false})
		return
	}
	status, err := s.Billing.Status(r.Context(), a.OrgID, a.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, status)
}
func (s *Server) billingOrder(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var o billing.OrderSummary
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT id,sku,currency,state,created_at,cancel_at,paid_through FROM billing_order_status WHERE id=$1 AND org_id=$2`, id, a.OrgID).Scan(&o.ID, &o.SKU, &o.Currency, &o.State, &o.Created, &o.CancelAt, &o.PaidThrough)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = apierr.ErrNotFound
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, o)
}
func (s *Server) billingBaseline(w http.ResponseWriter, r *http.Request, a *access) {
	var req struct{ Plan string }
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.Billing == nil || (req.Plan != "free" && req.Plan != "flex") {
		s.fail(w, r, apierr.New(422, "validation_error", "Choose Free or Flex."))
		return
	}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var changed bool
		if err := tx.QueryRow(r.Context(), `SELECT hedvi_select_billing_baseline($1,$2)`, a.OrgID, req.Plan).Scan(&changed); err != nil {
			return err
		}
		if !changed {
			return apierr.New(409, "billing_plan", "The selected baseline is unavailable. Flex requires available prepaid credit.")
		}
		return s.audit(r.Context(), tx, a, "billing.baseline_changed", "organization", a.OrgID.String(), map[string]any{"plan": req.Plan}, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"plan": req.Plan})
}

func (s *Server) billingCheckout(w http.ResponseWriter, r *http.Request, a *access) {
	if s.Billing == nil {
		s.fail(w, r, apierr.New(503, "billing_unavailable", "Hosted billing is unavailable."))
		return
	}
	var req struct {
		SKU, Currency string
		Profile       *billing.Profile `json:"billing_profile"`
		RequestID     uuid.UUID        `json:"request_id"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.Billing.Checkout(r.Context(), a.OrgID, a.UserID, a.ProjectID, req.RequestID, req.SKU, req.Currency, req.Profile)
	s.billingRedirect(w, r, a, u, err)
}
func (s *Server) billingPortal(w http.ResponseWriter, r *http.Request, a *access) {
	if s.Billing == nil {
		s.fail(w, r, apierr.New(503, "billing_unavailable", "Hosted billing is unavailable."))
		return
	}
	if err := decode(r, &struct{}{}); err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.Billing.Portal(r.Context(), a.OrgID, a.UserID, a.ProjectID)
	s.billingRedirect(w, r, a, u, err)
}
func (s *Server) billingRedirect(w http.ResponseWriter, r *http.Request, a *access, u string, err error) {
	if err != nil {
		var public *apierr.Error
		if !errors.As(err, &public) {
			s.Log.Warn("billing request could not complete", "err", err)
			err = apierr.New(503, "billing_unavailable", "Billing could not confirm the request. Check the order before retrying.")
		}
		s.fail(w, r, err)
		return
	}
	sess, err := s.loadSession(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !sess.LegalAccepted {
		s.fail(w, r, apierr.New(403, "legal_acknowledgement_required", "Review the current terms before continuing."))
		return
	}
	if sess.ID != a.ID || sess.UserID != a.UserID {
		s.fail(w, r, apierr.ErrNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"url": u})
}
