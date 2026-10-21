package facts

import (
	"encoding/json"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

type macStorageCapture struct {
	Volumes []struct {
		BSDName    string `json:"bsd_name"`
		FileSystem string `json:"file_system"`
		MountPoint string `json:"mount_point"`
		Size       uint64 `json:"size_in_bytes"`
		Drive      struct {
			Name         string `json:"device_name"`
			Internal     string `json:"is_internal_disk"`
			Media        string `json:"media_name"`
			MediumType   string `json:"medium_type"`
			PartitionMap string `json:"partition_map_type"`
			Protocol     string `json:"protocol"`
			SMARTStatus  string `json:"smart_status"`
		} `json:"physical_drive"`
	} `json:"SPStorageDataType"`
}

func TestMacStorageNestingFixture(t *testing.T) {
	var capture macStorageCapture
	if err := json.Unmarshal(hardwareReviewFixture(t, "mac-profiler-nesting.json"), &capture); err != nil {
		t.Fatal(err)
	}
	type capturedDrive struct {
		name, internal, media, mediumType, partitionMap, protocol, smart string
	}
	want := map[string]capturedDrive{
		"disk3s1":   {"APPLE SSD AP0256Q", "yes", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Apple Fabric", "Verified"},
		"disk3s3":   {"APPLE SSD AP0256Q", "yes", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Apple Fabric", "Verified"},
		"disk5s1":   {"Disk Image", "no", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Disk Image", ""},
		"disk3s3s1": {"APPLE SSD AP0256Q", "yes", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Apple Fabric", "Verified"},
	}
	if len(capture.Volumes) != len(want) {
		t.Fatalf("capture lost storage records: got %d, want %d", len(capture.Volumes), len(want))
	}
	seen := map[string]bool{}
	for _, volume := range capture.Volumes {
		w, exists := want[volume.BSDName]
		if seen[volume.BSDName] || !exists || volume.Drive.Name != w.name {
			t.Errorf("capture lost a volume/drive association: %+v", volume)
		}
		seen[volume.BSDName] = true
		if volume.FileSystem != "APFS" || volume.MountPoint == "" || volume.Size == 0 {
			t.Errorf("captured volume fields lost for %s: %+v", volume.BSDName, volume)
		}
		if volume.Drive.Internal != w.internal || volume.Drive.Media != w.media || volume.Drive.MediumType != w.mediumType || volume.Drive.PartitionMap != w.partitionMap || volume.Drive.Protocol != w.protocol || volume.Drive.SMARTStatus != w.smart {
			t.Errorf("captured provider metadata changed for %s: %+v", volume.BSDName, volume.Drive)
		}
	}
}

func macHardwareReadingsNamed(readings []models.HardwareReading, name string) []models.HardwareReading {
	var matches []models.HardwareReading
	for _, reading := range readings {
		if reading.Name == name {
			matches = append(matches, reading)
		}
	}
	return matches
}

func assertMacHardwareReading(t *testing.T, readings []models.HardwareReading, name string, want any) {
	t.Helper()
	matches := macHardwareReadingsNamed(readings, name)
	if len(matches) != 1 {
		t.Errorf("want exactly one %q reading, got %d: %+v", name, len(matches), matches)
		return
	}
	got := matches[0]
	if got.Value != want || got.Status != "observed" || got.Source != "system_profiler SPStorageDataType" {
		t.Errorf("%q: got %+v, want observed %v from system_profiler SPStorageDataType", name, got, want)
	}
}

// The target contract is documented before the parser implementation in
// docs/decisions/hardware-storage-parent-contract.md. Fixture containment is
// observed evidence; a backing-device identity is not present in this capture.
func TestMacStorageParentContract(t *testing.T) {
	c, report := testHardwareCollector(), testHardwareReport()
	for i := range report.Sections {
		report.Sections[i].Devices = nil
	}
	if err := c.parseMacHardwareInventory(hardwareReviewFixture(t, "mac-profiler-nesting.json"), report); err != nil {
		t.Fatal(err)
	}
	devices := hardwareSection(report, "Storage").Devices
	byID := map[string]models.HardwareDevice{}
	var descriptions []models.HardwareDevice
	for _, device := range devices {
		if device.ID == "" {
			t.Fatal("empty storage ID")
		}
		if _, exists := byID[device.ID]; exists {
			t.Fatalf("duplicate storage ID: %s", device.ID)
		}
		byID[device.ID] = device
		for _, reading := range device.Specs {
			if reading.Name == "device" {
				descriptions = append(descriptions, device)
			}
		}
	}
	if len(descriptions) != 4 {
		t.Fatalf("lost backing descriptions: got %d, want 4", len(descriptions))
	}
	type expectedDescription struct {
		name, internal, media, mediumType, partitionMap, protocol, smart string
	}
	want := map[string]expectedDescription{
		"disk3s1":   {"APPLE SSD AP0256Q", "yes", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Apple Fabric", "Verified"},
		"disk3s3":   {"APPLE SSD AP0256Q", "yes", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Apple Fabric", "Verified"},
		"disk5s1":   {"Disk Image", "no", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Disk Image", ""},
		"disk3s3s1": {"APPLE SSD AP0256Q", "yes", "AppleAPFSMedia", "ssd", "unknown_partition_map_type", "Apple Fabric", "Verified"},
	}
	parents := map[string]int{}
	for _, device := range descriptions {
		t.Run(device.ID, func(t *testing.T) {
			profilerParents := macHardwareReadingsNamed(device.Specs, "profiler parent")
			if len(profilerParents) != 1 {
				t.Errorf("want exactly one profiler parent, got %d: %+v", len(profilerParents), profilerParents)
			}
			legacyParents := macHardwareReadingsNamed(device.Specs, "parent")
			if len(legacyParents) != 0 {
				t.Errorf("storage description uses backing-parent label for profiler containment: %+v", legacyParents)
			}

			// Fall back to today's mislabeled edge only to identify which captured
			// description this red parser emitted. The assertion above still rejects it.
			var id string
			if len(profilerParents) == 1 {
				parent := profilerParents[0]
				if got, ok := parent.Value.(string); ok && parent.Status == "observed" && parent.Source == "system_profiler SPStorageDataType" {
					id = got
				} else {
					t.Errorf("invalid profiler containment reading: %+v", parent)
				}
			} else if len(legacyParents) == 1 {
				id, _ = legacyParents[0].Value.(string)
			}
			if id == "" {
				t.Errorf("description has no usable captured-volume association")
				return
			}
			expectation, exists := want[id]
			if !exists {
				t.Errorf("description points to unexpected captured volume %q", id)
				return
			}
			if id == device.ID {
				t.Errorf("profiler containment self-references %q", id)
			}
			if _, exists := byID[id]; !exists {
				t.Errorf("profiler parent %q has no record", id)
			}
			parents[id]++
			if device.Name != expectation.name {
				t.Errorf("description for %s named %q, want captured device_name %q", id, device.Name, expectation.name)
			}
			assertMacHardwareReading(t, device.Specs, "device", expectation.name)
			for _, field := range []struct {
				name, value string
			}{{"internal", expectation.internal}, {"media", expectation.media}, {"medium type", expectation.mediumType}, {"partition table", expectation.partitionMap}, {"protocol", expectation.protocol}} {
				assertMacHardwareReading(t, device.Specs, field.name, field.value)
			}
			if expectation.smart == "" {
				if got := macHardwareReadingsNamed(device.Specs, "SMART status"); len(got) != 0 {
					t.Errorf("capture has no SMART status for %s, parser emitted %+v", id, got)
				}
			} else {
				assertMacHardwareReading(t, device.Specs, "SMART status", expectation.smart)
			}
		})
	}
	for _, id := range []string{"disk3s1", "disk3s3", "disk5s1", "disk3s3s1"} {
		t.Run(id+"/backing-parent", func(t *testing.T) {
			if parents[id] != 1 {
				t.Errorf("want exactly one profiler description for %s, got %d", id, parents[id])
			}
			volume, exists := byID[id]
			if !exists {
				t.Fatalf("missing volume %s", id)
			}
			parentReadings := macHardwareReadingsNamed(volume.Specs, "parent")
			if len(parentReadings) != 1 {
				t.Fatalf("want exactly one backing parent reading, got %d: %+v", len(parentReadings), parentReadings)
			}
			parent := parentReadings[0]
			if parent.Status != "unavailable" || parent.Value != nil || parent.Source != "system_profiler SPStorageDataType" || parent.Reason != "backing device not identified by system_profiler" {
				t.Errorf("unproven backing parent must be explicit unknown: %+v", parent)
			}
		})
	}
}
