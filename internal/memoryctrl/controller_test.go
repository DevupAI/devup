package memoryctrl

import (
	"testing"

	"devup/internal/api"
)

func TestDefaultBudgetBytesRespectsProfile(t *testing.T) {
	total := int64(8 * 1024 * 1024 * 1024)

	batch := DefaultBudgetBytes(api.ProfileBatch, total)
	service := DefaultBudgetBytes(api.ProfileService, total)
	interactive := DefaultBudgetBytes(api.ProfileInteractive, total)

	if !(batch < service && service < interactive) {
		t.Fatalf("expected batch < service < interactive budgets, got %d %d %d", batch, service, interactive)
	}
}

func TestObserveKeepsHighAtBudgetWhenHostHasHeadroom(t *testing.T) {
	ctrl := New()

	limits := ctrl.Observe(Sample{
		Profile:            api.ProfileService,
		CurrentBytes:       512 * mib,
		MaxBytes:           1024 * mib,
		HostAvailableBytes: 3 * 1024 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	})

	if limits.HighBytes != 1024*mib {
		t.Fatalf("expected high to stay at hard limit, got %d", limits.HighBytes)
	}
	if limits.LowBytes == 0 {
		t.Fatal("expected a non-zero protected floor")
	}
}

func TestObserveConstrictsUnderPressure(t *testing.T) {
	ctrl := New()

	limits := ctrl.Observe(Sample{
		Profile:            api.ProfileBatch,
		CurrentBytes:       512 * mib,
		MaxBytes:           1024 * mib,
		HostAvailableBytes: 512 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	})

	if limits.HighBytes >= 1024*mib {
		t.Fatalf("expected high below max under pressure, got %d", limits.HighBytes)
	}
	if limits.ReclaimableBytes <= 0 {
		t.Fatalf("expected some reclaimable bytes under pressure, got %d", limits.ReclaimableBytes)
	}
}

func TestObserveInteractiveProtectsMoreThanBatch(t *testing.T) {
	batch := New().Observe(Sample{
		Profile:            api.ProfileBatch,
		CurrentBytes:       512 * mib,
		MaxBytes:           1024 * mib,
		HostAvailableBytes: 512 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	})
	interactive := New().Observe(Sample{
		Profile:            api.ProfileInteractive,
		CurrentBytes:       512 * mib,
		MaxBytes:           1024 * mib,
		HostAvailableBytes: 512 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	})

	if interactive.LowBytes <= batch.LowBytes {
		t.Fatalf("expected interactive floor to exceed batch floor, got batch=%d interactive=%d", batch.LowBytes, interactive.LowBytes)
	}
	batchProtectedPct := (batch.LowBytes * 100) / batch.HighBytes
	interactiveProtectedPct := (interactive.LowBytes * 100) / interactive.HighBytes
	if interactiveProtectedPct <= batchProtectedPct {
		t.Fatalf("expected interactive profile to protect a larger share of its budget, got batch=%d%% interactive=%d%%", batchProtectedPct, interactiveProtectedPct)
	}
}

func TestObserveUsesProfileBudgetWithoutHardLimit(t *testing.T) {
	ctrl := New()

	limits := ctrl.Observe(Sample{
		Profile:            api.ProfileBatch,
		CurrentBytes:       128 * mib,
		HostAvailableBytes: 2 * 1024 * mib,
		HostTotalBytes:     8 * 1024 * mib,
	})

	if limits.MaxBytes != 0 {
		t.Fatalf("expected unlimited hard max, got %d", limits.MaxBytes)
	}
	if limits.HighBytes != DefaultBudgetBytes(api.ProfileBatch, 8*1024*mib) {
		t.Fatalf("expected high to follow default budget, got %d", limits.HighBytes)
	}
}
