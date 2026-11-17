package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/capability"
	"github.com/toasterbook88/axis/internal/models"
)

func TestFactsHardwareCheckerVerdicts(t *testing.T) {
	with := func(change func(*hardwareReading)) hardwareReading {
		r := sampleHardware
		change(&r)
		return r
	}
	cases := []struct {
		name      string
		axis      *models.NodeFacts
		axisErr   error
		probe     hardwareReading
		probeErr  error
		want      capability.Outcome
		claimHas  string
		groundful bool
	}{
		{"agree", sampleFacts(sampleHardware), nil, sampleHardware, nil, capability.OutcomePass, "match the probe", true},
		{"arch spelled by uname", sampleFacts(with(func(r *hardwareReading) { r.Arch = "x86_64" })), nil, sampleHardware, nil, capability.OutcomePass, "arch=amd64", true},
		{"ram within rounding", sampleFacts(with(func(r *hardwareReading) { r.RAMTotalMB += 40 })), nil, sampleHardware, nil, capability.OutcomePass, "match", true},
		{"cores differ", sampleFacts(with(func(r *hardwareReading) { r.Cores = 4 })), nil, sampleHardware, nil, capability.OutcomeFail, "cores axis=4 probe=8", true},
		{"disk differs", sampleFacts(with(func(r *hardwareReading) { r.DiskTotalGB = 400 })), nil, sampleHardware, nil, capability.OutcomeFail, "disk_total_gb axis=400 probe=900", true},
		{"ram not observed", sampleFacts(with(func(r *hardwareReading) { r.RAMTotalMB = 0 })), nil, sampleHardware, nil, capability.OutcomeInconclusive, "did not observe ram_total_mb", true},
		{"probe failed", sampleFacts(sampleHardware), nil, hardwareReading{}, errors.New("no statfs"), capability.OutcomeInconclusive, "independent probe failed", false},
		{"collector failed", nil, errors.New("timeout"), sampleHardware, nil, capability.OutcomeInconclusive, "collector failed: timeout", false},
		{"collector gave no resources", &models.NodeFacts{Hostname: "alpha"}, nil, sampleHardware, nil, capability.OutcomeInconclusive, "no resources", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeHardware(t, tc.axis, tc.axisErr, tc.probe, tc.probeErr)
			obs := checkFactsHardware(context.Background(), "")
			if obs.Outcome != tc.want || !strings.Contains(obs.Claim, tc.claimHas) {
				t.Fatalf("outcome %s claim %q; want %s containing %q", obs.Outcome, obs.Claim, tc.want, tc.claimHas)
			}
			if obs.Checker != factsHardwareChecker || obs.Evidence.Kind != capability.EvidenceLiveProbe || obs.Evidence.Ref == "" {
				t.Fatalf("observation must name its checker and probe: %+v", obs)
			}
			if tc.groundful && (len(obs.Evidence.SHA256) != 64 || obs.Subject != "node:alpha") {
				t.Fatalf("a completed comparison must hash both readings and name the node: %+v", obs)
			}
		})
	}
}

func TestTheRealProbeReadsThisMachine(t *testing.T) {
	r, ref, err := probeHardware()
	if err != nil {
		t.Skipf("no independent probe here: %v", err)
	}
	if ref == "" || r.OS == "" || r.Arch == "" || r.Cores < 1 || r.RAMTotalMB < 1 || r.DiskTotalGB < 1 {
		t.Fatalf("probe = %+v (%s)", r, ref)
	}
}
