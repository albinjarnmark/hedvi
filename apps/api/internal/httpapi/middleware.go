package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/secure"
)

type ctxKey int

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

const requestIDKey ctxKey = 1

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Common wraps handlers with request ids, recovery, security headers, body
// limits and access logs. Logs never contain bodies, keys or cookies.
func Common(log *slog.Logger, maxBody int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-Id")
		if !validRequestID.MatchString(id) {
			id = "req_" + secure.RandomToken(10)
		}
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic", "request_id", id, "panic", p, "stack", string(debug.Stack()))
				if r.Method == http.MethodPost && (r.URL.Path == "/emails" || strings.HasSuffix(r.Pattern, "/emails/{id}/resend")) {
					apierr.Write(rec, apierr.ErrSendUncertain)
				} else {
					apierr.Write(rec, apierr.ErrInternal)
				}
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(rec.status/100)+"xx").Inc()
			metrics.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
			log.Info("http", "request_id", id, "method", r.Method, "route", route, "status", rec.status,
				"duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}
