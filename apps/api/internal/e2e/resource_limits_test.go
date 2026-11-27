package e2e

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/httpapi"
	"hedvi.dev/api/internal/ratelimit"
	"hedvi.dev/api/internal/testenv"
)

func TestConcurrentWebhookAndKeyCreationLimits(t *testing.T) {
	e := testenv.New(t)
	cookie, _, _, pid := signupForSecurity(t, e)
	// Exercise the durable creation budgets even if an ingress cache restarts
	// or requests are distributed across replicas. Console rate limits have a
	// separate regression; they must not mask the database concurrency invariant.
	h := authConsole(e, &config.Config{Env: config.EnvTest, AWSMode: "mock"}, permissiveAuthLimiter{})
	base := "/console/v1/projects/" + pid.String()
	for _, tc := range []struct {
		path         string
		body         map[string]any
		want, reject int
	}{
		{"/webhooks", map[string]any{"url": "https://example.com/events"}, 10, 422},
		{"/api-keys", map[string]any{"name": "rotation", "mode": "test", "permission": "sending_access"}, 19, 429},
	} {
		t.Run(tc.path, func(t *testing.T) {
			start := make(chan struct{})
			results := make(chan int, 25)
			var wg sync.WaitGroup
			for range 25 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- cookieRequest(h, cookie, "POST", base+tc.path, tc.body)
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			accepted := 0
			for code := range results {
				if code == 201 {
					accepted++
				} else if code != tc.reject {
					t.Fatalf("unexpected status %d", code)
				}
			}
			if accepted != tc.want {
				t.Fatalf("accepted %d, want %d", accepted, tc.want)
			}
		})
	}
	// Revoked keys still count toward the durable 24-hour creation budget.
	for i := 20; i < 50; i++ {
		e.Exec(`UPDATE api_keys SET revoked_at=now() WHERE project_id=$1`, pid)
		if code := cookieRequest(h, cookie, "POST", base+"/api-keys", map[string]any{"name": fmt.Sprintf("rotate-%d", i), "mode": "test"}); code != 201 {
			t.Fatal(code)
		}
	}
	e.Exec(`UPDATE api_keys SET revoked_at=now() WHERE project_id=$1`, pid)
	if code := cookieRequest(h, cookie, "POST", base+"/api-keys", map[string]any{"name": "over-budget", "mode": "test"}); code != 429 {
		t.Fatalf("revocation bypassed creation budget: %d", code)
	}
}

func TestAPIRateBudgetSharedAcrossKeysAndIsolatedByOrganization(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	mux := http.NewServeMux()
	api := httpapi.Server{Pool: e.APIPool, Hasher: e.Hasher, Limiter: ratelimit.NewMemory(), Log: slog.Default(), KeyRate: 0.001, KeyBurst: 2}
	api.Routes(mux)
	for _, key := range []string{a.TestKey, a.LiveKey} {
		if code, body, _ := call(t, mux, "GET", "/emails", key, nil, nil); code != 200 {
			t.Fatal(code, body)
		}
	}
	if code, _, _ := call(t, mux, "GET", "/emails", a.TestKey, nil, nil); code != 429 {
		t.Fatalf("key multiplication bypassed org budget: %d", code)
	}
	if code, _, _ := call(t, mux, "GET", "/emails", b.TestKey, nil, nil); code != 200 {
		t.Fatalf("other org shared rate budget: %d", code)
	}
}
