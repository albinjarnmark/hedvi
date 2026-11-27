package webhooks

import (
	"testing"
	"time"
)

// Vector from the Standard Webhooks specification test suite.
func TestSignMatchesStandardWebhooksVector(t *testing.T) {
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	ts := time.Unix(1614265330, 0)
	body := []byte(`{"test": 2432232314}`)
	got, err := Sign(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", ts, body)
	if err != nil {
		t.Fatal(err)
	}
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Fatalf("signature %s, want %s", got, want)
	}
	if err := Verify(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", "1614265330", "v1,bad "+got, body, ts.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := Verify(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", "1614265330", got, body, ts.Add(10*time.Minute)); err == nil {
		t.Fatal("replay outside tolerance accepted")
	}
}
