package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/testenv"
)

func TestDomainRequestOptionsAndRequestIDs(t *testing.T) {
	e := testenv.New(t)
	tenant := e.NewTenant("free")
	for _, raw := range []string{
		`{"name":"options.test","capabilities":{"receiving": "enabled"}}`,
		`{"name":"options.test","capabilities":{"receiving":"wrong"}}`,
		`{"name":"options.test","capabilities":{"sending":"disabled"}}`,
		`{"name":"options.test","capabilities":{"unknown":true}}`,
		`{"name":"options.test","tls":"invalid"}`,
		`{"name":"options.test"}]`,
		`{"name":"options.test"}{}`,
	} {
		r := httptest.NewRequest("POST", "/domains", strings.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+tenant.LiveKey)
		w := httptest.NewRecorder()
		e.Handler.ServeHTTP(w, r)
		if w.Code != 422 {
			t.Errorf("expected 422 for %s, got %d", raw, w.Code)
		}
	}
	status, _, header := call(t, e.Handler, "POST", "/domains", tenant.LiveKey, map[string]any{"name": "options.test", "capabilities": map[string]string{"receiving": "disabled", "sending": "enabled"}, "tls": "opportunistic"}, map[string]string{"X-Request-Id": "unsafe value"})
	if status != 200 || !strings.HasPrefix(header.Get("X-Request-Id"), "req_") {
		t.Fatalf("valid options/request ID %d %v", status, header)
	}
	_, _, header = call(t, e.Handler, "GET", "/healthz", "", nil, map[string]string{"X-Request-Id": "a.B_1-2"})
	if header.Get("X-Request-Id") != "a.B_1-2" {
		t.Fatal("valid ID replaced")
	}
}

func TestTestAndMonthlyBudgetsRejectBeforeUpload(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	body := liveBody(tn, "budget@example.org")
	store := &observedPayloadStore{Store: e.Blobs}
	e.Accept.Blobs = store
	e.Exec(`UPDATE projects SET test_daily_limit=1 WHERE id=$1`, tn.ProjectID)
	if _, err := e.Accept.Send(e.Ctx, tn.Test, "", body); err != nil {
		t.Fatal(err)
	}
	_, err := e.Accept.Send(e.Ctx, tn.Test, "", body)
	if apiErr(t, err).Status != 429 || store.puts.Load() != 1 {
		t.Fatal("daily capture preflight", err, store.puts.Load())
	}
	e.Exec(`UPDATE projects SET test_daily_limit=20,test_storage_limit_bytes=(SELECT sum(payload_bytes) FROM emails WHERE project_id=$1)+1 WHERE id=$1`, tn.ProjectID)
	_, err = e.Accept.Send(e.Ctx, tn.Test, "", body)
	if apiErr(t, err).Status != 422 || store.puts.Load() != 1 {
		t.Fatal("capture bytes preflight", err, store.puts.Load())
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", body); err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE usage_periods SET included=reserved WHERE org_id=$1`, tn.OrgID)
	_, err = e.Accept.Send(e.Ctx, tn.Live, "", body)
	if apiErr(t, err).Name != "monthly_quota_exceeded" || store.puts.Load() != 2 {
		t.Fatal("monthly preflight", err, store.puts.Load())
	}
}

func TestDomainRevocationDuringUploadBlocksAcceptance(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	arrived := make(chan struct{})
	release := make(chan struct{})
	e.Accept.Blobs = &observedPayloadStore{Store: e.Blobs, arrived: arrived, release: release}
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(e.Ctx, 5*time.Second)
	defer cancel()
	go func() { _, err := e.Accept.Send(ctx, tn.Live, "", liveBody(tn, "policy@example.org")); result <- err }()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("upload did not start")
	}
	e.Exec(`UPDATE domains SET status='pending',verified_at=NULL WHERE project_id=$1`, tn.ProjectID)
	close(release)
	if err := <-result; apiErr(t, err).Status != 403 || e.Count(`SELECT count(*) FROM emails`) != 0 || storedObjects(t, e) != 0 {
		t.Fatal("revoked domain accepted", err)
	}
}

func TestConsoleResendAndDeletedCaptureCount(t *testing.T) {
	e := testenv.New(t)
	cookie, _, org, pid := signupForSecurity(t, e)
	base := "/console/v1/projects/" + pid.String()
	p := accept.Principal{OrgID: org, ProjectID: pid, Mode: "test", Permission: "full_access"}
	body := []byte(`{"from":"a@example.org","to":"b@example.org","subject":"123","text":"a"}`)
	original, err := e.Accept.Send(e.Ctx, p, "", body)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Cookie": cookie.String(), "X-Hedvi-Console": "1", "Idempotency-Key": "deliberate-action"}
	for range 2 {
		status, _, _ := call(t, e.Handler, "POST", base+"/emails/"+original.EmailID.String()+"/resend", "", nil, headers)
		if status != 201 {
			t.Fatal("resend status", status)
		}
	}
	if e.Count(`SELECT count(*) FROM emails WHERE resend_of=$1`, original.EmailID) != 1 || e.Count(`SELECT count(*) FROM audit_log WHERE action='email.resent'`) != 1 {
		t.Fatal("resend not atomic and idempotent")
	}
	failure := errors.New("cannot audit")
	_, err = e.Accept.Resend(e.Ctx, p, original.EmailID, "reject-audit", body, func(pgx.Tx, uuid.UUID) error { return failure })
	if !errors.Is(err, failure) || e.Count(`SELECT count(*) FROM emails`) != 2 {
		t.Fatal("audit failure accepted email", err)
	}
	e.Exec(`UPDATE emails SET payload_ref=NULL,content_deleted_at=now() WHERE id=$1`, original.EmailID)
	status, result, _ := call(t, e.Handler, "DELETE", base+"/test-inbox/"+original.EmailID.String(), "", nil, headers)
	if status != 200 || result["deleted"] != float64(1) {
		t.Fatal(status, result)
	}
}

type failedCounterPool struct {
	db.Beginner
	err error
}

func (p failedCounterPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Beginner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failedCounterTx{tx, p.err}, nil
}

type failedCounterTx struct {
	pgx.Tx
	err error
}
type counterQueryError struct{ error }

func (e counterQueryError) Scan(...any) error { return e.error }
func (tx failedCounterTx) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	if strings.HasPrefix(q, "INSERT INTO send_counters") {
		return counterQueryError{tx.err}
	}
	return tx.Tx.QueryRow(ctx, q, args...)
}
func TestCaptureCounterDatabaseFailureIsNotQuota(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	fail := errors.New("counter unavailable")
	e.Accept.Pool = failedCounterPool{e.APIPool, fail}
	_, err := e.Accept.Send(e.Ctx, tn.Test, "", liveBody(tn, "x@example.org"))
	if !errors.Is(err, fail) || e.Count(`SELECT count(*) FROM emails`) != 0 {
		t.Fatalf("counter failure: %v", err)
	}
}
