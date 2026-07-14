package events

import "github.com/toasterbook88/axis/internal/reservation"

func init() {
	reservation.SetEventEmitter(func(name string, payload map[string]any) {
		Emit(NoopEmitter{}, name, payload)
	})
}
