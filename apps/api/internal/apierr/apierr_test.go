package apierr

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFailuresMatchOperationWithoutLeakingProviderDetails(t *testing.T) {
	secret := errors.New("AWS AccessDeniedException resource private-tenant secret-value")
	for _, tc := range []struct {
		name   string
		write  func(*httptest.ResponseRecorder)
		status int
		send   bool
	}{
		{"general", func(w *httptest.ResponseRecorder) { Write(w, secret) }, 500, false},
		{"domain", func(w *httptest.ResponseRecorder) { WriteDomainCheck(w, secret) }, 503, false},
		{"sending", func(w *httptest.ResponseRecorder) { WriteSend(w, secret) }, 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.write(w)
			body := w.Body.String()
			if w.Code != tc.status || strings.Contains(body, "secret-value") || strings.Contains(body, "AccessDenied") {
				t.Fatal(w.Code, body)
			}
			if strings.Contains(body, "Idempotency-Key") != tc.send {
				t.Fatal("incorrect retry guidance", body)
			}
			if tc.name == "domain" && w.Header().Get("Retry-After") != "30" {
				t.Fatal("missing bounded retry guidance")
			}
		})
	}
	w := httptest.NewRecorder()
	WriteDomainCheck(w, ErrNotFound)
	if w.Code != 404 {
		t.Fatal("typed validation error changed")
	}
}
