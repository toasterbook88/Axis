package facts

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestWithResidentPort(t *testing.T) {
	t.Run("stamps port on models with none", func(t *testing.T) {
		rms := []models.ResidentModel{
			{Name: "a", Runtime: "llama.cpp"},
			{Name: "b", Runtime: "llama.cpp"},
		}
		got := withResidentPort(rms, 8080)
		for _, rm := range got {
			if rm.Port != 8080 {
				t.Errorf("model %q port = %d, want 8080", rm.Name, rm.Port)
			}
		}
	})

	t.Run("preserves explicit per-model port", func(t *testing.T) {
		rms := []models.ResidentModel{{Name: "a", Port: 9999}}
		got := withResidentPort(rms, 8080)
		if got[0].Port != 9999 {
			t.Errorf("port = %d, want 9999 (preserved)", got[0].Port)
		}
	})

	t.Run("no-op on non-positive port", func(t *testing.T) {
		rms := []models.ResidentModel{{Name: "a"}}
		got := withResidentPort(rms, 0)
		if got[0].Port != 0 {
			t.Errorf("port = %d, want 0", got[0].Port)
		}
	})
}
