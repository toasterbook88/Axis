package models

const MinimumSystemReserveMB int64 = 1024

// SystemReserveMB returns the protected system headroom AXIS keeps out of the
// shared reservable pool on a node.
func SystemReserveMB(totalRAMMB int64) int64 {
	if totalRAMMB <= 0 {
		return 0
	}
	if totalRAMMB < MinimumSystemReserveMB {
		return totalRAMMB
	}
	return MinimumSystemReserveMB
}

// ReservableRAMMB returns the node RAM budget AXIS is willing to treat as part
// of the shared cluster pool before subtracting local reservations. It is
// bounded by both live free RAM and the protected system reserve floor.
func ReservableRAMMB(totalRAMMB, freeRAMMB int64) int64 {
	if freeRAMMB <= 0 {
		return 0
	}
	if totalRAMMB <= 0 {
		return freeRAMMB
	}

	capMB := totalRAMMB - SystemReserveMB(totalRAMMB)
	if capMB < 0 {
		capMB = 0
	}
	if freeRAMMB < capMB {
		return freeRAMMB
	}
	return capMB
}

// AllocatableRAMMB returns the currently allocatable RAM after subtracting
// locally persisted reservations from the reservable pool.
func AllocatableRAMMB(totalRAMMB, freeRAMMB, reservedRAMMB int64) int64 {
	if reservedRAMMB < 0 {
		reservedRAMMB = 0
	}
	allocatable := ReservableRAMMB(totalRAMMB, freeRAMMB) - reservedRAMMB
	if allocatable < 0 {
		return 0
	}
	return allocatable
}

// SystemReserveMBWithConfig returns the protected system headroom, using the
// per-node config value when it is > 0, otherwise falling back to the global
// MinimumSystemReserveMB. The result is capped at totalRAMMB.
func SystemReserveMBWithConfig(totalRAMMB, configReserveMB int64) int64 {
	if totalRAMMB <= 0 {
		return 0
	}
	reserve := configReserveMB
	if reserve <= 0 {
		reserve = MinimumSystemReserveMB
	}
	if reserve > totalRAMMB {
		return totalRAMMB
	}
	return reserve
}

// ReservableRAMMBWithConfig is like ReservableRAMMB but respects a per-node
// system reserve override when configReserveMB > 0.
func ReservableRAMMBWithConfig(totalRAMMB, freeRAMMB, configReserveMB int64) int64 {
	if freeRAMMB <= 0 {
		return 0
	}
	if totalRAMMB <= 0 {
		return freeRAMMB
	}
	capMB := totalRAMMB - SystemReserveMBWithConfig(totalRAMMB, configReserveMB)
	if capMB < 0 {
		capMB = 0
	}
	if freeRAMMB < capMB {
		return freeRAMMB
	}
	return capMB
}

// AllocatableRAMMBWithConfig is like AllocatableRAMMB but respects a per-node
// system reserve override when configReserveMB > 0.
func AllocatableRAMMBWithConfig(totalRAMMB, freeRAMMB, reservedRAMMB, configReserveMB int64) int64 {
	if reservedRAMMB < 0 {
		reservedRAMMB = 0
	}
	allocatable := ReservableRAMMBWithConfig(totalRAMMB, freeRAMMB, configReserveMB) - reservedRAMMB
	if allocatable < 0 {
		return 0
	}
	return allocatable
}
