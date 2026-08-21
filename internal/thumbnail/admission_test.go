package thumbnail

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDecodeAdmissionDefaultClosed(t *testing.T) {
	if got := NewDecodeAdmission(0); got != nil {
		t.Fatal("zero per-tenant limit must leave admission disabled")
	}
	if got := NewPerTenantDecodeAdmission(-1); got != nil {
		t.Fatal("negative per-tenant limit must leave admission disabled")
	}
	a := NewDecodeAdmission(maxConcurrentDecodes + 1)
	if a == nil || a.perTenant != maxConcurrentDecodes {
		t.Fatalf("admission limit = %v, want clamped %d", a, maxConcurrentDecodes)
	}
}

func TestDecodeAdmissionProtectsOtherTenantSlot(t *testing.T) {
	a := NewDecodeAdmission(maxConcurrentDecodes - 1)
	held := make([]func(), maxConcurrentDecodes-1)
	for i := range held {
		release, err := a.Acquire(context.Background(), "tenant-a")
		if err != nil {
			t.Fatalf("tenant-a acquire %d: %v", i, err)
		}
		held[i] = release
	}
	defer func() {
		for _, release := range held {
			release()
		}
		assertSlotsReleased(t)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := a.Acquire(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("tenant-b should receive the remaining global slot: %v", err)
	}
	release()

	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	if _, err := a.Acquire(short, "tenant-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fourth tenant-a slot error = %v, want deadline exceeded", err)
	}
}

func TestDecodeAdmissionCancellationReleasesTenantReservation(t *testing.T) {
	a := NewDecodeAdmission(1)
	first, err := a.Acquire(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Acquire(ctx, "tenant-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tenant acquire = %v, want context.Canceled", err)
	}
	first()
	second, err := a.Acquire(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("tenant reservation was not released after cancellation: %v", err)
	}
	second()
	assertSlotsReleased(t)
}

func TestDecodeAdmissionGlobalCancellationReleasesTenantReservation(t *testing.T) {
	slotSaturate()
	defer func() {
		for len(decodeSlots) > 0 {
			releaseDecodeSlot()
		}
		recoverSlots(t)
	}()
	a := NewDecodeAdmission(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := a.Acquire(ctx, "tenant-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("global wait error = %v, want deadline exceeded", err)
	}

	releaseDecodeSlot()
	second, err := a.Acquire(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("tenant reservation after global cancellation: %v", err)
	}
	second()
}
