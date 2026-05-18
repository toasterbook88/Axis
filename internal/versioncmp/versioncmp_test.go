package versioncmp

import (
	"testing"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    int
		wantErr bool
	}{
		{"equal", "v0.10.2", "v0.10.2", 0, false},
		{"older", "v0.9.0", "v0.10.2", -1, false},
		{"newer", "v0.11.0", "v0.10.2", 1, false},
		{"current missing v", "0.10.2", "v0.10.2", 0, false},
		{"latest missing v", "v0.10.2", "0.10.2", 0, false},
		{"both missing v", "0.10.2", "0.10.2", 0, false},
		{"short current", "v0.10", "v0.10.0", 0, false},
		{"short latest", "v0.10.0", "v0.10", 0, false},
		{"prerelease older", "v0.10.2-alpha", "v0.10.2", -1, false},
		{"prerelease newer", "v0.10.2", "v0.10.2-alpha", 1, false},
		{"empty current", "", "v0.10.2", 0, true},
		{"empty latest", "v0.10.2", "", 0, true},
		{"invalid current", "not-a-version", "v0.10.2", 0, true},
		{"invalid latest", "v0.10.2", "not-a-version", 0, true},
		{"too many segments", "v0.10.2.3", "v0.10.2", 0, true},
		{"major diff", "v1.0.0", "v0.10.2", 1, false},
		{"minor diff", "v0.9.0", "v0.10.0", -1, false},
		{"patch diff", "v0.10.1", "v0.10.2", -1, false},
		{"with whitespace", " v0.10.2 ", "v0.10.2", 0, false},
		{"with build meta", "v0.10.2+build.1", "v0.10.2", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Compare(tt.current, tt.latest)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Compare(%q, %q) error = %v, wantErr %v", tt.current, tt.latest, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"simple", "v1.2.3", "v1.2.3", false},
		{"missing v", "1.2.3", "v1.2.3", false},
		{"short", "v1.2", "v1.2.0", false},
		{"single", "v1", "v1.0.0", false},
		{"empty", "", "", true},
		{"whitespace", " 1.2.3 ", "v1.2.3", false},
		{"prerelease", "v1.2.3-alpha", "v1.2.3-alpha", false},
		{"build meta", "v1.2.3+build", "v1.2.3+build", false},
		{"prerelease and build", "v1.2.3-alpha+build", "v1.2.3-alpha+build", false},
		{"too many", "v1.2.3.4", "", true},
		{"invalid", "abc", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
