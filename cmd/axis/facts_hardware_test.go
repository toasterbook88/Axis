package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/models"
	"gopkg.in/yaml.v3"
)

type fixtureHardwareSampler struct {
	calls  int
	cancel context.CancelFunc
}

func (s *fixtureHardwareSampler) Sample(ctx context.Context, _ time.Duration) (*models.HardwareReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.calls++
	if s.cancel != nil && s.calls == 2 {
		s.cancel()
	}
	return &models.HardwareReport{SchemaVersion: "axis.hardware/v1", OS: "darwin", Arch: "arm64", Sections: []models.HardwareSection{{Name: "CPU", Devices: []models.HardwareDevice{{ID: "cpu", Name: "Fixture CPU"}}}}}, nil
}
func stubHardwareSampler(t *testing.T, s hardwareSampler) {
	t.Helper()
	old := newHardwareSampler
	newHardwareSampler = func() hardwareSampler { return s }
	t.Cleanup(func() { newHardwareSampler = old })
}

func TestHardwareShortcutIsScopedAndPreservesHelp(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{[]string{"facts", "-hw"}, []string{"facts", "--hardware"}},
		{[]string{"--no-color", "node", "facts", "-hw", "-h"}, []string{"--no-color", "node", "facts", "--hardware", "-h"}},
		{[]string{"agent", "-hw"}, []string{"agent", "-hw"}},
		{[]string{"facts", "--", "-hw"}, []string{"facts", "--", "-hw"}},
		{[]string{"facts", "--format", "-hw"}, []string{"facts", "--format", "-hw"}},
		{[]string{"facts", "--format=-hw"}, []string{"facts", "--format=-hw"}},
	} {
		if got := normalizeHardwareArgs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v => %v want %v", tc.in, got, tc.want)
		}
	}
	for _, flag := range []string{"-h", "--help"} {
		cmd := factsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{flag})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--hardware", "-hw", "--watch", "--interval", "macOS and Linux"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("help missing %s", want)
			}
		}
	}
}

func TestHardwareFactsBypassesGeneralCollector(t *testing.T) {
	old := collectLocalFacts
	collectLocalFacts = func(context.Context, string) (*models.NodeFacts, error) {
		t.Fatal("hardware used general facts collector")
		return nil, nil
	}
	t.Cleanup(func() { collectLocalFacts = old })
	for _, format := range []string{"text", "json", "yaml"} {
		sampler := &fixtureHardwareSampler{}
		stubHardwareSampler(t, sampler)
		cmd := factsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--hardware", "--format", format})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if sampler.calls != 1 {
			t.Fatalf("calls %d", sampler.calls)
		}
		switch format {
		case "json":
			var r models.HardwareReport
			if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.OS != "darwin" {
				t.Fatalf("JSON %v %s", err, out.String())
			}
		case "yaml":
			var r models.HardwareReport
			if err := yaml.Unmarshal(out.Bytes(), &r); err != nil || r.Arch != "arm64" {
				t.Fatalf("YAML %v", err)
			}
		}
	}
}

func TestHardwareFlagValidationAndNodeAlias(t *testing.T) {
	for _, args := range [][]string{{"--watch"}, {"--interval", "1s"}, {"--hardware", "--interval", "0s"}, {"--hardware", "--interval", "2m"}, {"--hardware", "extra"}, {"--hardware", "--format", "invalid"}} {
		cmd := factsCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	sampler := &fixtureHardwareSampler{}
	stubHardwareSampler(t, sampler)
	cmd := nodeCmd()
	cmd.SetOut(io.Discard)
	cmd.SetArgs(normalizeHardwareArgs([]string{"facts", "-hw"}))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if sampler.calls != 1 {
		t.Fatal("node facts alias did not collect hardware")
	}
}

func TestHardwareWatchStructuredFramesAndCancellation(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := &fixtureHardwareSampler{cancel: cancel}
		stubHardwareSampler(t, s)
		cmd := factsCmd()
		cmd.SetContext(ctx)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--hardware", "--watch", "--format", format})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		count := 0
		if format == "json" {
			dec := json.NewDecoder(&out)
			for {
				var v models.HardwareReport
				err := dec.Decode(&v)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				count++
			}
		} else {
			dec := yaml.NewDecoder(&out)
			for {
				var v models.HardwareReport
				err := dec.Decode(&v)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				count++
			}
		}
		if count != 2 {
			t.Fatalf("%s frames: %d", format, count)
		}
	}
}

type hardwareFailWriter struct{}

func (hardwareFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestHardwareOutputFailureAndMissingReading(t *testing.T) {
	r := &models.HardwareReport{Sections: []models.HardwareSection{{Name: "GPU", Devices: []models.HardwareDevice{{Name: "GPU", Metrics: []models.HardwareReading{{Name: "load", Status: "permission_denied", Reason: "read permission denied", Source: "test"}, {Name: "idle", Value: float64(0), Unit: "%", Status: "observed", Source: "test"}}}}}}}
	var out bytes.Buffer
	if err := printHardwareFacts(&out, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "permission_denied") || !strings.Contains(out.String(), "0.00 %") {
		t.Fatal(out.String())
	}
	if err := printHardwareFacts(hardwareFailWriter{}, r); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer failure: %v", err)
	}
	s := &fixtureHardwareSampler{}
	stubHardwareSampler(t, s)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(hardwareFailWriter{})
	if err := runHardwareFacts(cmd, "json", false, time.Second); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("JSON writer failure: %v", err)
	}
}
