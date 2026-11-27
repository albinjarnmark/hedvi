// Package plans holds plan allowances and the fixed hosted billing catalog.
package plans

import (
	"fmt"
	"time"
)

const (
	MB = int64(1 << 20)
	GB = int64(1 << 30)
)

type Plan struct {
	ID                 string
	Name               string
	IncludedRecipients int64 // per verified paid cycle or UTC baseline month
	IncludedDataBytes  int64 // outgoing attachment bytes per month
	MaxDomains         int
	MetadataDays       int
	MaxContentDays     int
	Team               bool
	Webhooks           bool
	OverageAllowed     bool
	PriceUSDCents      int64
	PriceSEKOre        int64
}

var All = map[string]Plan{
	"free":     {ID: "free", Name: "Free", IncludedRecipients: 5_000, IncludedDataBytes: 100 * MB, MaxDomains: 3, MetadataDays: 14, MaxContentDays: 7, Webhooks: true},
	"flex":     {ID: "flex", Name: "Flex", IncludedRecipients: 0, IncludedDataBytes: 0, MaxDomains: 10, MetadataDays: 30, MaxContentDays: 7, Webhooks: true},
	"pro_50k":  {ID: "pro_50k", Name: "Pro 50k", IncludedRecipients: 50_000, IncludedDataBytes: 1 * GB, MaxDomains: 25, MetadataDays: 90, MaxContentDays: 7, Team: true, Webhooks: true, PriceUSDCents: 1300, PriceSEKOre: 12900},
	"pro_100k": {ID: "pro_100k", Name: "Pro 100k", IncludedRecipients: 100_000, IncludedDataBytes: 2 * GB, MaxDomains: 100, MetadataDays: 90, MaxContentDays: 7, Team: true, Webhooks: true, PriceUSDCents: 2500, PriceSEKOre: 24900},
	"custom":   {ID: "custom", Name: "Custom", IncludedRecipients: 0, IncludedDataBytes: 0, MaxDomains: 100, MetadataDays: 90, MaxContentDays: 7, Team: true, Webhooks: true},
}

// Flex credit pack: prepaid, no subscription, 365-day validity.
const (
	FlexPackRecipients = 10_000
	FlexPackDataBytes  = 250 * MB
	FlexPackValidity   = 365 * 24 * time.Hour
	FlexPackUSDCents   = 400
	FlexPackSEKOre     = 3900
)

// Test inbox limits apply to every plan and never touch live quota.
const (
	TestRetention = 7 * 24 * time.Hour
)

func Get(id string) (Plan, error) {
	p, ok := All[id]
	if !ok {
		return Plan{}, fmt.Errorf("unknown plan %q", id)
	}
	return p, nil
}

// Retention returns effective metadata and content retention for an org.
// Content retention can be shortened by the customer but never exceeds the
// plan maximum; zero means "delete after processing completes".
func Retention(p Plan, metadataOverride, contentOverride *int32) (metadata, content time.Duration) {
	md := p.MetadataDays
	if metadataOverride != nil && int(*metadataOverride) < md {
		md = int(*metadataOverride)
	}
	cd := p.MaxContentDays
	if contentOverride != nil && int(*contentOverride) < cd {
		cd = int(*contentOverride)
	}
	return time.Duration(md) * 24 * time.Hour, time.Duration(cd) * 24 * time.Hour
}

// MonthBounds returns the UTC calendar month containing t.
func MonthBounds(t time.Time) (start, end time.Time) {
	t = t.UTC()
	start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}
