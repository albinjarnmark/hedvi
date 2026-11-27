package testenv

import (
	"bytes"
	"io"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
)

// AuthToken reads the actual simulated email, never a token from HTTP or the
// challenge table. Only isolated test databases have localmock.ses_outbound.
func (e *Env) AuthToken(email string) string {
	e.T.Helper()
	if err := e.AuthMail.RunOnce(e.Ctx); err != nil {
		e.T.Fatal(err)
	}
	var raw []byte
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT raw FROM localmock.ses_outbound WHERE $1=ANY(recipients) AND tags ? 'hedvi_auth' ORDER BY created_at DESC LIMIT 1`, email).Scan(&raw); err != nil {
		e.T.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		e.T.Fatal(err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
	if err != nil {
		e.T.Fatal(err)
	}
	found := regexp.MustCompile(`#token=([a-z0-9-]+\.[a-z2-7]+)`).FindSubmatch(body)
	if len(found) != 2 {
		e.T.Fatal("simulated account mail has no token fragment")
	}
	return string(found[1])
}
