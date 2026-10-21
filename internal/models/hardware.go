package models

import "time"

// HardwareReport is a local, read-only observation, independent of placement
// policy, configured cluster inventory, and inference runtime discovery.
type HardwareReport struct {
	SchemaVersion string            `json:"schema_version" yaml:"schema_version"`
	Hostname      string            `json:"hostname" yaml:"hostname"`
	OS            string            `json:"os" yaml:"os"`
	Arch          string            `json:"arch" yaml:"arch"`
	Status        string            `json:"status" yaml:"status"`
	InventoryAt   time.Time         `json:"inventory_at" yaml:"inventory_at"`
	CollectedAt   time.Time         `json:"collected_at" yaml:"collected_at"`
	SampleStart   time.Time         `json:"sample_start" yaml:"sample_start"`
	SampleEnd     time.Time         `json:"sample_end" yaml:"sample_end"`
	SampleSeconds float64           `json:"sample_seconds" yaml:"sample_seconds"`
	Sections      []HardwareSection `json:"sections" yaml:"sections"`
}

type HardwareSection struct {
	Name    string           `json:"name" yaml:"name"`
	Devices []HardwareDevice `json:"devices" yaml:"devices"`
}

// ID is a local component address (e.g. cpu0 or a PCI bus address), never a
// serial number, MAC address, machine UUID, or disk UUID.
type HardwareDevice struct {
	ID      string            `json:"id" yaml:"id"`
	Name    string            `json:"name" yaml:"name"`
	Specs   []HardwareReading `json:"specs" yaml:"specs"`
	Metrics []HardwareReading `json:"metrics" yaml:"metrics"`
}

// HardwareReading keeps unknown distinct from a measured zero. Value is a
// string, number, or bool only when Status is observed or derived. Rate fields
// carry their source's sample window; point readings carry their observation time.
type HardwareReading struct {
	Name          string     `json:"name" yaml:"name"`
	Value         any        `json:"value,omitempty" yaml:"value,omitempty"`
	Unit          string     `json:"unit,omitempty" yaml:"unit,omitempty"`
	Source        string     `json:"source" yaml:"source"`
	Status        string     `json:"status" yaml:"status"`
	Reason        string     `json:"reason,omitempty" yaml:"reason,omitempty"`
	SampleStart   *time.Time `json:"sample_start,omitempty" yaml:"sample_start,omitempty"`
	SampleEnd     *time.Time `json:"sample_end,omitempty" yaml:"sample_end,omitempty"`
	SampleSeconds float64    `json:"sample_seconds,omitempty" yaml:"sample_seconds,omitempty"`
	ObservedAt    time.Time  `json:"observed_at" yaml:"observed_at"`
}
