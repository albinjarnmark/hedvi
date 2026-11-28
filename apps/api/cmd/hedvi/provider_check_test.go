package main

import "testing"

func TestSenderCanaryRejectsIncompleteOrInjectedConfiguration(t *testing.T) {
	project := "00000000-0000-4000-8000-000000000001"
	claim := "00000000-0000-4000-8000-000000000002"
	for _, args := range [][]string{
		{"--sender-project=" + project},
		{"--recipient=customer@example.com"},
		{"unexpected"},
		{"--sender-project=" + project, "--sender-domain-claim=" + claim, "--sender-from=Hedvi <a@example.com>"},
		{"--sender-project=" + project, "--sender-domain-claim=" + claim, "--sender-from=a@example.com\r\nBcc: other@example.com"},
		{"--sender-project=00000000-0000-0000-0000-000000000000", "--sender-domain-claim=" + claim, "--sender-from=a@example.com"},
	} {
		if _, err := parseSenderCanary(args); err == nil {
			t.Fatalf("accepted invalid sender check: %q", args)
		}
	}
	got, err := parseSenderCanary([]string{"--sender-project=" + project, "--sender-domain-claim=" + claim, "--sender-from=canary@pilot.example.com"})
	if err != nil || got.domain != "pilot.example.com" || got.project.String() != project || got.claim.String() != claim {
		t.Fatalf("valid sender check: %v %v", got, err)
	}
	if got, err := parseSenderCanary(nil); err != nil || got != nil {
		t.Fatal("API provisioning-only check changed", got, err)
	}
}
