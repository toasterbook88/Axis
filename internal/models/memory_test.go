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
