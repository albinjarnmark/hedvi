package domains

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/db"
)

// A custom MAIL FROM host needs one exact MX. Adding it to a host with
// implicit MX (A/AAAA), another provider's MX/SPF, or an alias can redirect mail.
func inspectMailFrom(ctx context.Context, r Resolver, mx, spf *Record) string {
	name := mx.FQDN + "."
	mxs, mxErr := r.LookupMX(ctx, name)
	mx.Found = nil
	for _, entry := range mxs {
		if len(mx.Found) < 16 {
			mx.Found = append(mx.Found, fmt.Sprintf("%d %s", entry.Pref, displayDNS(strings.TrimSuffix(entry.Host, "."))))
		}
	}
	mxOK := len(mxs) == 1 && mx.Priority != nil && int(mxs[0].Pref) == *mx.Priority && strings.EqualFold(mxs[0].Host, mx.Value+".")
	mx.Status = statusFor(mxErr, mxOK)
	target, addrErr := r.LookupCNAME(ctx, name)
	// The Go resolver also returns the canonical name for an A/AAAA owner.
	occupied := addrErr == nil && target != "" && (!strings.EqualFold(target, name) || len(mxs) == 0)
	txts, txtErr := r.LookupTXT(ctx, name)
	spf.Found = nil
	var spfCount int
	ourSPF := false
	for _, txt := range txts {
		if len(spf.Found) < 16 {
			spf.Found = append(spf.Found, displayDNS(txt))
		}
		fields := strings.Fields(txt)
		if len(fields) != 0 && strings.EqualFold(fields[0], "v=spf1") {
			spfCount++
			ourSPF = ourSPF || strings.TrimSpace(txt) == spf.Value
		}
	}
	spfOK := spfCount == 1 && ourSPF
	spf.Status = statusFor(txtErr, spfOK)
	if occupied || len(mxs) > 0 && !mxOK || spfCount > 0 && !spfOK {
		if occupied {
			mx.Status = "pending"
		}
		return "conflict"
	}
	for _, err := range []error{mxErr, addrErr, txtErr} {
		if err != nil && !isNotFound(err) {
			mx.Status = "temporary_failure"
			return "unknown"
		}
	}
	return "clear"
}

// Check the current public DNS immediately before preparing an import or
// switching to another bounce address. Recursors can still cache DNS changes.
func (s *Service) checkMailFrom(ctx context.Context, d *Domain, label string) error {
	host := label + "." + d.Name
	if !ValidMailFromLabel(label) || len(host) > 253 {
		return apierr.New(422, "validation_error", "Enter one valid DNS label for the MAIL FROM subdomain.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case checkSlots <- struct{}{}:
		defer func() { <-checkSlots }()
	case <-ctx.Done():
		return apierr.New(503, "domain_check_unavailable", "DNS is busy. Try again shortly.")
	}
	mx := Record{FQDN: host, Value: s.MailFromMX, Priority: intp(10)}
	spf := Record{FQDN: host, Value: "v=spf1 include:amazonses.com ~all"}
	switch inspectMailFrom(ctx, s.resolver(d), &mx, &spf) {
	case "conflict":
		return apierr.New(409, "mail_from_conflict", "%s already has mail or address records. Keep them and choose an unused MAIL FROM subdomain.", host)
	case "unknown":
		return apierr.New(503, "domain_check_unavailable", "DNS could not confirm the MAIL FROM records. Try again shortly.")
	}
	return nil
}

func (s *Service) PrepareDNSExport(ctx context.Context, org, project, id uuid.UUID) (*Domain, error) {
	d, err := s.Get(ctx, org, project, id)
	if err != nil {
		return nil, err
	}
	if d.ProviderState == "deleting" {
		return nil, apierr.New(409, "validation_error", "This domain is being removed.")
	}
	if len(d.Records) == 1 && d.Records[0].Record == "Ownership" {
		return d, nil
	}
	if err = s.checkMailFrom(ctx, d, d.MailFrom); err != nil {
		return nil, err
	}
	current, err := s.Get(ctx, org, project, id)
	if err != nil {
		return nil, err
	}
	if current.MailFromRevision != d.MailFromRevision || current.ProviderState != d.ProviderState {
		return nil, apierr.New(409, "validation_error", "The domain changed. Refresh before downloading its DNS records.")
	}
	d.Analysis.MailFromSafety = "clear"
	return d, nil
}

func (s *Service) ChangeMailFrom(ctx context.Context, org, project, id, user uuid.UUID, previous, next string, revision int64) (*Domain, error) {
	d, err := s.Get(ctx, org, project, id)
	if err != nil {
		return nil, err
	}
	if s.reservedAuth(d.Name) || d.ProviderState == "deleting" {
		return nil, apierr.New(409, "validation_error", "This domain cannot be changed.")
	}
	if next == d.MailFrom {
		return d, nil
	}
	if previous != d.MailFrom || revision != d.MailFromRevision {
		return nil, apierr.New(409, "validation_error", "The MAIL FROM subdomain changed. Refresh the domain before trying again.")
	}
	if err := s.checkMailFrom(ctx, d, next); err != nil {
		return nil, err
	}
	err = db.InOrg(ctx, s.Pool, org, func(tx pgx.Tx) error {
		var changed bool
		if err := tx.QueryRow(ctx, `SELECT hedvi_change_mail_from($1,$2,$3,$4,$5)`, project, id, previous, revision, next).Scan(&changed); err != nil {
			return err
		}
		if !changed {
			return apierr.New(409, "validation_error", "The domain changed or another operation is running. Refresh and retry.")
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_log(org_id,actor_kind,actor_user_id,action,target_type,target_id,metadata) VALUES ($1,'user',$2,'domain.mail_from_changed','domain',$3,$4)`, org, user, id.String(), map[string]string{"previous": previous, "current": next})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, org, project, id)
}
