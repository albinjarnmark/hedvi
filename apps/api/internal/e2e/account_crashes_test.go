package e2e

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hedvi.dev/api/internal/authmail"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/testenv"
	"io"
	"log/slog"
	"net/http"
	"testing"
)

type authLostAck struct {
	Pool     *pgxpool.Pool
	Rollback bool
}

func (a authLostAck) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &authLostAckTx{Tx: tx, rollback: a.Rollback}, nil
}

type authLostAckTx struct {
	pgx.Tx
	rollback bool
}

func (t *authLostAckTx) Commit(ctx context.Context) error {
	var err error
	if t.rollback {
		err = t.Tx.Rollback(ctx)
	} else {
		err = t.Tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	return errors.New("simulated lost transaction acknowledgement")
}

func TestAuthMailLostAdmissionCommitDoesNotDeleteCommittedJob(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "rolledback"}[rollback], func(t *testing.T) {
			e := testenv.New(t)
			s := &authmail.Service{Pool: e.APIPool, Hasher: e.Hasher, Sealer: e.Sealer, Begin: authLostAck{Pool: e.APIPool, Rollback: rollback}}
			if err := s.Request(e.Ctx, "signup", "lost@example.test", "en", "192.0.2.1"); err == nil {
				t.Fatal("injected acknowledgement was not lost")
			}
			want := 1
			if rollback {
				want = 0
			}
			if e.Count(`SELECT count(*) FROM auth_mail_outbox`) != want || e.Count(`SELECT count(*) FROM auth_challenges`) != want {
				t.Fatal("ambiguous admission compensated or partially committed")
			}
			if !rollback {
				token := e.AuthToken("lost@example.test")
				if c, b, _ := authRequest(t, e, "signup/complete", map[string]any{"token": token, "password": "owner-password-123", "consent": consent()}); c != 201 {
					t.Fatal(c, b)
				}
			}
		})
	}
}

func TestAuthSignupLostCommitLeavesExactlyOneRecoverableAccount(t *testing.T) {
	e := testenv.New(t)
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "signup-ack@example.test"})
	token := e.AuthToken("signup-ack@example.test")
	mux := http.NewServeMux()
	s := &console.Server{Cfg: &config.Config{Env: config.EnvTest}, Pool: e.APIPool, Hasher: e.Hasher, Sealer: e.Sealer, Limiter: permissiveAuthLimiter{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), AuthBegin: authLostAck{Pool: e.APIPool}}
	s.Routes(mux)
	body := map[string]any{"token": token, "password": "owner-password-123", "consent": consent()}
	if c, _, _ := authCall(t, mux, nil, "/console/v1/auth/signup/complete", body); c != 503 {
		t.Fatal(c)
	}
	if e.Count(`SELECT count(*) FROM users`) != 1 || e.Count(`SELECT count(*) FROM sessions`) != 1 {
		t.Fatal("committed account lost")
	}
	if c, _, _ := authRequest(t, e, "signup/complete", body); c != 400 {
		t.Fatal("proof replay minted credentials", c)
	}
	if c, b, _ := authRequest(t, e, "login", map[string]any{"email": "signup-ack@example.test", "password": "owner-password-123"}); c != 200 {
		t.Fatal("cannot recover via normal login", c, b)
	}
}

func TestAuthWorkerClaimLostAckAndCrashNeverBlindlyRetry(t *testing.T) {
	for _, point := range []string{"lost_claim", faults.AuthAfterClaim, faults.AuthAfterProvider} {
		t.Run(point, func(t *testing.T) {
			e := testenv.New(t)
			_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "crash@example.test"})
			if point == "lost_claim" {
				e.AuthMail.ClaimBegin = authLostAck{Pool: e.WorkerPool}
			} else {
				e.Faults.Arm(point, 1)
			}
			if err := e.AuthMail.RunOnce(e.Ctx); err == nil {
				t.Fatal("fault did not trigger")
			}
			sent := 0
			if point == faults.AuthAfterProvider {
				sent = 1
			}
			if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != sent {
				t.Fatal("sent despite unconfirmed claim")
			}
			e.AuthMail.ClaimBegin = nil
			e.Exec(`UPDATE auth_mail_outbox SET lease_until=now()-interval '1 second' WHERE state='sending'`)
			if err := e.AuthMail.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != sent || e.Count(`SELECT count(*) FROM auth_mail_outbox WHERE state='unknown' AND payload_enc IS NULL`) != 1 {
				t.Fatal("expired attempt retried or retained token payload")
			}
		})
	}
}

func TestAuthLateComplaintUsesPseudonymousCorrelation(t *testing.T) {
	e := testenv.New(t)
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "late+complaint@example.test"})
	_ = e.AuthToken("late+complaint@example.test")
	e.Exec(`UPDATE auth_challenges SET created_at=now()-interval '10 days',expires_at=now()-interval '10 days'+interval '30 minutes'`)
	if err := e.AuthMail.Cleanup(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM auth_challenges WHERE email_enc=''::bytea AND user_id IS NULL`) != 1 || e.Count(`SELECT count(*) FROM auth_mail_outbox WHERE payload_enc IS NULL`) != 1 {
		t.Fatal("expired identity/link not erased")
	}
	// The simulator's queued send event is still bound after the original proof
	// expired; late complaint validation is separately exercised in boundaries.
	e.Drain()
	if e.Count(`SELECT count(*) FROM auth_mail_suppressions WHERE reason='complaint'`) != 1 {
		t.Fatal("late bound complaint was discarded")
	}
	if e.Count(`SELECT count(*) FROM auth_mail_outbox WHERE result_code='event_confirmed'`) != 1 {
		t.Fatal("late bound event was discarded")
	}
}
