package models_test

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestSystemReserveMBUsesProtectedFloor(t *testing.T) {
	if got := models.SystemReserveMB(8192); got != 1024 {
		t.Fatalf("SystemReserveMB(8192) = %d, want 1024", got)
	}
	if got := models.SystemReserveMB(768); got != 768 {
		t.Fatalf("SystemReserveMB(768) = %d, want 768", got)
	}
}

func TestReservableRAMMBHonorsLiveFreeAndSystemReserve(t *testing.T) {
	if got := models.ReservableRAMMB(8192, 7900); got != 7168 {
		t.Fatalf("ReservableRAMMB(8192, 7900) = %d, want 7168", got)
	}
	if got := models.ReservableRAMMB(8192, 3072); got != 3072 {
		t.Fatalf("ReservableRAMMB(8192, 3072) = %d, want 3072", got)
	}
}

func TestAllocatableRAMMBSubtractsReservationsFromReservablePool(t *testing.T) {
	if got := models.AllocatableRAMMB(8192, 7900, 512); got != 6656 {
		t.Fatalf("AllocatableRAMMB(8192, 7900, 512) = %d, want 6656", got)
	}
	if got := models.AllocatableRAMMB(2048, 1500, 4096); got != 0 {
		t.Fatalf("AllocatableRAMMB should floor at 0, got %d", got)
	}
}

func TestSystemReserveMBWithConfig(t *testing.T) {
	if got := models.SystemReserveMBWithConfig(8192, 0); got != 1024 {
		t.Fatalf("SystemReserveMBWithConfig(8192, 0) = %d, want 1024", got)
	}
	if got := models.SystemReserveMBWithConfig(8192, 2048); got != 2048 {
		t.Fatalf("SystemReserveMBWithConfig(8192, 2048) = %d, want 2048", got)
	}
	if got := models.SystemReserveMBWithConfig(512, 2048); got != 512 {
		t.Fatalf("SystemReserveMBWithConfig(512, 2048) = %d, want 512", got)
	}
	if got := models.SystemReserveMBWithConfig(0, 2048); got != 0 {
		t.Fatalf("SystemReserveMBWithConfig(0, 2048) = %d, want 0", got)
	}
}

func TestReservableRAMMBWithConfig(t *testing.T) {
	if got := models.ReservableRAMMBWithConfig(8192, 7900, 0); got != 7168 {
		t.Fatalf("ReservableRAMMBWithConfig(8192, 7900, 0) = %d, want 7168", got)
	}
	if got := models.ReservableRAMMBWithConfig(8192, 7900, 2048); got != 6144 {
		t.Fatalf("ReservableRAMMBWithConfig(8192, 7900, 2048) = %d, want 6144", got)
	}
	if got := models.ReservableRAMMBWithConfig(8192, 3072, 2048); got != 3072 {
		t.Fatalf("ReservableRAMMBWithConfig(8192, 3072, 2048) = %d, want 3072", got)
	}
}

func TestAllocatableRAMMBWithConfig(t *testing.T) {
	if got := models.AllocatableRAMMBWithConfig(8192, 7900, 512, 2048); got != 5632 {
		t.Fatalf("AllocatableRAMMBWithConfig(8192, 7900, 512, 2048) = %d, want 5632", got)
	}
	if got := models.AllocatableRAMMBWithConfig(2048, 1500, 4096, 512); got != 0 {
		t.Fatalf("AllocatableRAMMBWithConfig should floor at 0, got %d", got)
	}
}
