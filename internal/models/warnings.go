package models

// AppendWarningIfMissing appends warning to snap only if an identical warning
// (kind/message/node) is not already present.
func AppendWarningIfMissing(snap *ClusterSnapshot, warning Warning) {
	if snap == nil {
		return
	}
	for _, existing := range snap.Warnings {
		if existing.Kind == warning.Kind && existing.Message == warning.Message && existing.Node == warning.Node {
			return
		}
	}
	snap.Warnings = append(snap.Warnings, warning)
}
