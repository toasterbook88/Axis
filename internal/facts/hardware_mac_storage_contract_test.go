package facts

import (
	"encoding/json"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

type macStorageCapture struct {
	Volumes []struct {
		BSDName string `json:"bsd_name"`
		Drive   struct {
			Name     string `json:"device_name"`
			Protocol string `json:"protocol"`
		} `json:"physical_drive"`
	} `json:"SPStorageDataType"`
}

func TestMacStorageNestingFixture(t *testing.T) {
	var capture macStorageCapture
	if err := json.Unmarshal(hardwareReviewFixture(t, "mac-profiler-nesting.json"), &capture); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"disk3s1": "APPLE SSD AP0256Q", "disk3s3": "APPLE SSD AP0256Q",
		"disk5s1": "Disk Image", "disk3s3s1": "APPLE SSD AP0256Q",
	}
	if len(capture.Volumes) != len(want) {
		t.Fatalf("capture lost storage records: got %d, want %d", len(capture.Volumes), len(want))
	}
	seen := map[string]bool{}
	for _, volume := range capture.Volumes {
		if seen[volume.BSDName] || want[volume.BSDName] == "" || volume.Drive.Name != want[volume.BSDName] {
			t.Errorf("capture lost a volume/drive association: %+v", volume)
		}
		seen[volume.BSDName] = true
		if volume.BSDName == "disk5s1" && volume.Drive.Protocol != "Disk Image" {
			t.Errorf("virtual backing description lost: %+v", volume)
		}
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
	parents := map[string]int{}
	for _, device := range descriptions {
		t.Run(device.ID, func(t *testing.T) {
			name := findHardwareReading(t, device.Specs, "device").Value
			if device.Name != name {
				t.Errorf("backing description has generic name %q, want %q", device.Name, name)
			}
			for _, reading := range device.Specs {
				if reading.Name == "parent" && reading.Value != nil {
					t.Errorf("profiler containment mislabeled as backing parent: %+v", reading)
				}
			}
			parent := findHardwareReading(t, device.Specs, "profiler parent")
			id, ok := parent.Value.(string)
			if !ok || parent.Status != "observed" || parent.Source != "system_profiler SPStorageDataType" || id == device.ID {
				t.Fatalf("invalid profiler containment: %+v", parent)
			}
			if _, exists := byID[id]; !exists {
				t.Errorf("profiler parent %q has no record", id)
			}
			parents[id]++
			if name == "Disk Image" && id != "disk5s1" {
				t.Errorf("disk-image description moved to unrelated volume %s", id)
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
			parent := findHardwareReading(t, volume.Specs, "parent")
			if parent.Status != "unavailable" || parent.Value != nil || parent.Source != "system_profiler SPStorageDataType" || parent.Reason != "backing device not identified by system_profiler" {
				t.Errorf("unproven backing parent must be explicit unknown: %+v", parent)
			}
		})
	}
}
