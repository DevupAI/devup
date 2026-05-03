package admission

import (
	"testing"

	"devup/internal/api"
)

func TestPlanAdmitsWhenHostHasHeadroom(t *testing.T) {
	decision := Plan(Request{
		Profile:            api.ProfileService,
		DemandBytes:        512 * mib,
		HostAvailableBytes: 2 * 1024 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	}, nil)

	if !decision.Admit {
		t.Fatalf("expected admit, got reject: %s", decision.Reason)
	}
	if len(decision.Adjustments) != 0 {
		t.Fatalf("expected no adjustments, got %d", len(decision.Adjustments))
	}
}

func TestPlanReclaimsBatchBeforeInteractive(t *testing.T) {
	decision := Plan(Request{
		Profile:            api.ProfileService,
		DemandBytes:        768 * mib,
		HostAvailableBytes: 300 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	}, []RunningJob{
		{
			JobID:     "interactive",
			Profile:   api.ProfileInteractive,
			LowBytes:  512 * mib,
			HighBytes: 900 * mib,
		},
		{
			JobID:     "batch",
			Profile:   api.ProfileBatch,
			LowBytes:  128 * mib,
			HighBytes: 700 * mib,
		},
	})

	if !decision.Admit {
		t.Fatalf("expected admit after reclaim, got reject: %s", decision.Reason)
	}
	if len(decision.Adjustments) == 0 {
		t.Fatal("expected reclaim adjustments")
	}
	if decision.Adjustments[0].JobID != "batch" {
		t.Fatalf("expected batch to be reclaimed first, got %s", decision.Adjustments[0].JobID)
	}
}

func TestPlanRejectsWhenReclaimIsInsufficient(t *testing.T) {
	decision := Plan(Request{
		Profile:            api.ProfileService,
		DemandBytes:        1024 * mib,
		HostAvailableBytes: 128 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	}, []RunningJob{
		{
			JobID:     "service",
			Profile:   api.ProfileService,
			LowBytes:  512 * mib,
			HighBytes: 640 * mib,
		},
	})

	if decision.Admit {
		t.Fatal("expected rejection when reclaim is insufficient")
	}
	if decision.Reason == "" {
		t.Fatal("expected rejection reason")
	}
}

func TestSlotsFreeUsesMemoryAndCount(t *testing.T) {
	slots := SlotsFree(2*1024*mib, 8*1024*mib, 2, 32)
	if slots <= 0 {
		t.Fatalf("expected positive slots, got %d", slots)
	}

	if slots := SlotsFree(2*1024*mib, 8*1024*mib, 32, 32); slots != 0 {
		t.Fatalf("expected zero slots when count cap reached, got %d", slots)
	}
}
