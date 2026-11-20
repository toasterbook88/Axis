package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// Thresholds for diskSpaceDoctorCheck: a root volume is low below 10% free,
// the kubelet's default nodefs.available eviction threshold, and critically
// low below 3%, node_exporter's critical NodeFilesystemAlmostOutOfSpace level.
const (
	diskLowPercent      = 10
	diskCriticalPercent = 3
)

// diskSpaceDoctorCheck warns about nodes whose root volume is nearly full,
// from the disk facts already in the snapshot. The collectors round both
// figures down to whole GiB, so a node is listed only when its share stays
// below the threshold with up to a whole GiB added to its free space: a listed
// node is really low, and a small volume just under the line may go unlisted.
func diskSpaceDoctorCheck(nodes []models.NodeFacts) DoctorCheck {
	type lowNode struct {
		name        string
		free, total int64
		critical    bool
	}
	var lows []lowNode
	var noFacts []string
	checked := 0
	for _, n := range nodes {
		if n.Resources == nil || n.Resources.DiskTotalGB <= 0 {
			noFacts = append(noFacts, n.Name)
			continue
		}
		checked++
		free, total := max(n.Resources.DiskFreeGB, 0), n.Resources.DiskTotalGB
		if (free+1)*100 < diskLowPercent*total {
			lows = append(lows, lowNode{name: n.Name, free: free, total: total,
				critical: (free+1)*100 < diskCriticalPercent*total})
		}
	}
	sort.Strings(noFacts)
	missing := ""
	if len(noFacts) > 0 {
		missing = "; no disk facts for " + strings.Join(noFacts, ", ")
	}
	if checked == 0 {
		return DoctorCheck{Name: "Disk space", Status: "warn", Message: "no disk facts in the snapshot",
			Fix: "run axis status to refresh the snapshot"}
	}
	if len(lows) == 0 {
		return DoctorCheck{Name: "Disk space", Status: "pass",
			Message: fmt.Sprintf("none of %d node(s) with disk facts is below %d%% free on its root volume%s",
				checked, diskLowPercent, missing)}
	}
	sort.Slice(lows, func(i, j int) bool {
		// Lowest free share first, compared without rounding; ties by name.
		a, b := lows[i].free*lows[j].total, lows[j].free*lows[i].total
		if a != b {
			return a < b
		}
		return lows[i].name < lows[j].name
	})
	parts := make([]string, 0, len(lows))
	for _, l := range lows {
		part := fmt.Sprintf("%s %d of %d GB free (%d%%)", l.name, l.free, l.total, l.free*100/l.total)
		if l.critical {
			part += ", critically low"
		}
		parts = append(parts, part)
	}
	return DoctorCheck{Name: "Disk space", Status: "warn", Message: strings.Join(parts, "; ") + missing,
		Fix: "free space on the root volume of the listed nodes; axis status --format json lists each node's volumes"}
}
