package domains

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type ownershipResolver struct {
	Resolver
	name string
	txt  []string
	err  error
}

func (r *ownershipResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if name == r.name {
		return r.txt, r.err
	}
	return r.Resolver.LookupTXT(ctx, name)
}

func TestOwnershipRequiresExactCurrentClaimAndSuccessfulAbsoluteLookup(t *testing.T) {
	claim := uuid.New()
	want := OwnershipRecord("mail.example.com", claim)
	for _, tc := range []struct {
		name string
		txt  []string
		err  error
		ok   bool
	}{
		{"current claim", []string{want.Value}, nil, true},
		{"old claim", []string{"hedvi-domain=" + uuid.NewString()}, nil, false},
		{"missing", nil, notFound(want.FQDN), false},
		{"padded", []string{" " + want.Value + " "}, nil, false},
		{"partial result with resolver error", []string{want.Value}, errors.New("temporary DNS failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := []Record{want}
			r := &ownershipResolver{Resolver: fakeResolver{}, name: want.FQDN + ".", txt: tc.txt, err: tc.err}
			Check(context.Background(), r, "mail.example.com", records)
			providerVerified := true
			if got := Overall(records, &providerVerified) == "verified"; got != tc.ok {
				t.Fatalf("verified=%v for %+v", got, records)
			}
		})
	}
}

func TestOwnershipGuideReplacesStaleClaimAndRequiresProof(t *testing.T) {
	claim := uuid.New()
	old := OwnershipRecord("example.com", uuid.New())
	old.Status = "verified"
	for _, stored := range [][]Record{nil, {old}, {old, old}} {
		got := withOwnershipRecord(stored, "example.com", claim)
		if len(got) != 1 || got[0].Value != "hedvi-domain="+claim.String() || !got[0].Required || got[0].Status != "not_started" {
			t.Fatalf("wrong ownership guide: %+v", got)
		}
	}
}
