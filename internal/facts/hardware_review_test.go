package facts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func hardwareReviewFixture(t testing.TB, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "hardware-review", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHardwareReviewSMARTFailedProbeWithStubFields(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		exit         int
		err          error
		messages     bool
	}{
		{"captured permission plus stub", "permission_denied", 2, errors.New("exit status 2"), true},
		{"wrapped permission", "permission_denied", 0, fmt.Errorf("device: %w", fs.ErrPermission), false},
		{"device open failed", "unavailable", 2, errors.New("exit status 2"), false},
		{"invalid command", "unavailable", 1, errors.New("exit status 1"), false},
		{"deadline", "timeout", 0, context.DeadlineExceeded, false},
		{"cancelled", "cancelled", 0, context.Canceled, false},
		{"genuine failing health", "observed", 8, errors.New("exit status 8"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(hardwareReviewFixture(t, "smart-permission.json"), &doc); err != nil {
				t.Fatal(err)
			}
			meta := doc["smartctl"].(map[string]any)
			meta["exit_status"] = tc.exit
			if !tc.messages {
				delete(meta, "messages")
			}
			// Synthetic stub fields added to a captured failed-open response.
			doc["smart_status"] = map[string]any{"passed": false}
			doc["temperature"] = map[string]any{"current": 0}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			c, r := testHardwareCollector(), testHardwareReport()
			d := hardwareDevice("sda", "disk")
			d.Specs = append(d.Specs, c.reading("type", "disk", "", "fixture"))
			hardwareSection(r, "Storage").Devices = []models.HardwareDevice{d}
			c.run = func(context.Context, string, ...string) ([]byte, error) { return raw, tc.err }
			c.linuxStorageHealth(context.Background(), r)
			metrics := hardwareSection(r, "Storage").Devices[0].Metrics
			if tc.status == "observed" {
				v := findHardwareReading(t, metrics, "smart_status passed")
				if v.Status != "observed" || v.Value != false {
					t.Fatalf("real failing health suppressed: %+v", v)
				}
			} else if len(metrics) != 1 || metrics[0].Status != tc.status || metrics[0].Value != nil || metrics[0].Reason == "" {
				t.Fatalf("failed probe promoted stub fields: %+v", metrics)
			}
		})
	}
}

func TestHardwareReviewMacEmptyDeviceIdentities(t *testing.T) {
	for _, tc := range []struct {
		typ, key, override string
	}{
		{"SPNetworkDataType", "interface", ""},
		{"SPStorageDataType", "bsd_name", ""},
		{"SPNetworkDataType", "interface", "   "},
	} {
		t.Run(tc.typ+"/"+tc.key+"="+tc.override, func(t *testing.T) {
			var doc map[string][]map[string]any
			if err := json.Unmarshal(hardwareReviewFixture(t, "mac-profiler.json"), &doc); err != nil {
				t.Fatal(err)
			}
			rows := doc[tc.typ][:2]
			for _, row := range rows {
				row[tc.key] = tc.override
			}
			raw, _ := json.Marshal(map[string]any{tc.typ: rows})
			c, r := testHardwareCollector(), testHardwareReport()
			for i := range r.Sections {
				r.Sections[i].Devices = nil
			}
			if err := c.parseMacHardwareInventory(raw, r); err != nil {
				t.Fatal(err)
			}
			section := "Network"
			if tc.typ == "SPStorageDataType" {
				section = "Storage"
			}
			devices := hardwareSection(r, section).Devices
			if len(devices) != 2 || devices[0].ID != tc.typ+"-0" || devices[1].ID != tc.typ+"-1" {
				t.Fatalf("empty identities merged distinct devices: %+v", devices)
			}
		})
	}
}

func TestHardwareReviewGPUVendorProbeSelection(t *testing.T) {
	var captured struct {
		ID, Vendor string
		Files      map[string]struct {
			Value string
			Errno int
		}
	}
	if err := json.Unmarshal(hardwareReviewFixture(t, "pci-nvidia.json"), &captured); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, vendor string
		amd          bool
	}{
		{"captured NVIDIA without vendor tool", captured.Vendor, false},
		{"synthetic Intel", "0x8086", false},
		{"synthetic AMD control", "0x1002", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r := testHardwareCollector(), testHardwareReport()
			d := hardwareDevice(captured.ID, tc.name)
			d.Specs = append(d.Specs, c.reading("vendor", tc.vendor, "", "fixture"))
			hardwareSection(r, "GPU").Devices = []models.HardwareDevice{d}
			amdFiles := []string{"gpu_busy_percent", "mem_info_vram_total", "mem_info_vram_used"}
			var reads []string
			c.read = func(path string) ([]byte, error) {
				name := filepath.Base(path)
				reads = append(reads, name)
				if tc.amd && slices.Contains(amdFiles, name) {
					return []byte("0"), nil
				}
				if field, ok := captured.Files[name]; ok && field.Errno == 0 {
					return []byte(field.Value), nil
				}
				return nil, fs.ErrNotExist
			}
			c.linuxGPUMetrics(context.Background(), r)
			for _, name := range amdFiles {
				if slices.Contains(reads, name) != tc.amd {
					t.Errorf("wrong vendor sysfs probes: %v", reads)
				}
			}
			for _, name := range []string{"current_link_speed", "current_link_width"} {
				if !slices.Contains(reads, name) {
					t.Errorf("PCIe probe lost: %s", name)
				}
			}
			if tc.amd {
				v := findHardwareReading(t, hardwareSection(r, "GPU").Devices[0].Metrics, "GPU utilization")
				if v.Status != "observed" || v.Value != float64(0) {
					t.Fatalf("AMD measured zero lost: %+v", v)
				}
			}
		})
	}
}

func TestHardwareReviewCapturedMacInventory(t *testing.T) {
	c, r := testHardwareCollector(), testHardwareReport()
	for i := range r.Sections {
		r.Sections[i].Devices = nil
	}
	if err := c.parseMacHardwareInventory(hardwareReviewFixture(t, "mac-profiler.json"), r); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"Network", "Storage"} {
		seen := map[string]bool{}
		for _, d := range hardwareSection(r, section).Devices {
			if d.ID == "" || seen[d.ID] {
				t.Fatalf("invalid replayed identity: %+v", d)
			}
			seen[d.ID] = true
		}
	}
}
