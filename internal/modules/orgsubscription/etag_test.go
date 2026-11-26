package orgsubscription

import (
	"strings"
	"testing"
	"time"
)

func TestStrongTagIsStableAndSensitive(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	trial := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	created := time.Date(2025, 12, 1, 10, 30, 0, 0, time.UTC)
	updated := time.Date(2026, 1, 2, 10, 30, 0, 0, time.UTC)

	base := Representation{
		ID:                 "01930000-0000-7000-8000-000000000001",
		Plan:               PlanPro,
		Status:             StatusActive,
		CurrentPeriodStart: &start,
		CurrentPeriodEnd:   &end,
		TrialEndsAt:        &trial,
		CreatedAt:          created,
		UpdatedAt:          updated,
	}

	tag := StrongTag(base)
	if !strings.HasPrefix(tag, ETagPrefix) {
		t.Fatalf("tag %q lacks prefix %q", tag, ETagPrefix)
	}
	if again := StrongTag(base); again != tag {
		t.Fatalf("tag changed between calls: %q != %q", again, tag)
	}

	changes := []struct {
		name string
		edit func(*Representation)
	}{
		{"id", func(rep *Representation) { rep.ID = "01930000-0000-7000-8000-000000000002" }},
		{"plan", func(rep *Representation) { rep.Plan = PlanEnterprise }},
		{"status", func(rep *Representation) { rep.Status = StatusPastDue }},
		{"period start", func(rep *Representation) { shifted := start.Add(time.Minute); rep.CurrentPeriodStart = &shifted }},
		{"period end", func(rep *Representation) { shifted := end.Add(time.Minute); rep.CurrentPeriodEnd = &shifted }},
		{"trial", func(rep *Representation) { rep.TrialEndsAt = nil }},
		{"canceled", func(rep *Representation) { rep.CanceledAt = &updated }},
		{"created", func(rep *Representation) { rep.CreatedAt = created.Add(time.Second) }},
		{"updated", func(rep *Representation) { rep.UpdatedAt = updated.Add(time.Second) }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			edited := base
			change.edit(&edited)
			if got := StrongTag(edited); got == tag {
				t.Fatalf("tag did not change after editing %s", change.name)
			}
		})
	}
}

func TestStrongTagNormalizesUTC(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	local := start.In(time.FixedZone("UTC+2", 2*60*60))
	created := time.Date(2025, 12, 1, 10, 0, 0, 0, time.UTC)

	utc := Representation{
		ID:                 "01930000-0000-7000-8000-000000000001",
		Plan:               PlanFree,
		Status:             StatusTrialing,
		CurrentPeriodStart: &start,
		CreatedAt:          created,
		UpdatedAt:          created,
	}
	withZone := utc
	withZone.CurrentPeriodStart = &local
	withZone.CreatedAt = created.In(time.FixedZone("UTC-5", -5*60*60))
	withZone.UpdatedAt = withZone.CreatedAt

	if StrongTag(utc) != StrongTag(withZone) {
		t.Fatal("equal instants with different zones produced different tags")
	}
}

func TestRepresentNormalizesOptionalTimes(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	record := Record{CurrentPeriodStart: &start}
	rep := record.Represent()
	if rep.CurrentPeriodStart == nil || rep.CurrentPeriodStart.Location() != time.UTC {
		t.Fatalf("current period start = %v, want UTC", rep.CurrentPeriodStart)
	}
	if rep.CurrentPeriodEnd != nil || rep.TrialEndsAt != nil || rep.CanceledAt != nil {
		t.Fatal("nil optional times must stay nil")
	}
}
