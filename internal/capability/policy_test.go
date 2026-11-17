package capability

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capabilities.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPoliciesFillsDefaultsAndFallsBack(t *testing.T) {
	ps, err := LoadPolicies(writePolicy(t, "capabilities:\n  - id: facts.hardware\n    rung: 1\n    promote_after: 30\n    window: 60\n"))
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	if got := ps.For("facts.hardware").Thresholds(); got != (Thresholds{PromoteAfter: 30, Window: 60, MinRate: DefaultMinRate, MinSpanMinutes: 360}) {
		t.Fatalf("thresholds = %+v", got)
	}
	if got := ps.For("facts.hardware").Rung; got != 1 {
		t.Fatalf("rung = %d", got)
	}
	if other := ps.For("health.service").Thresholds(); other != (Thresholds{DefaultPromoteAfter, DefaultWindow, DefaultMinRate, 360}) {
		t.Fatalf("fallback thresholds = %+v", other)
	}
}

func TestAPolicySpanIsParsed(t *testing.T) {
	ps, err := LoadPolicies(writePolicy(t, "capabilities:\n  - id: a\n    min_span: 12h\n"))
	if err != nil || ps.For("a").Thresholds().MinSpan() != 12*time.Hour {
		t.Fatalf("min_span 12h = %+v, %v", ps.For("a").Thresholds(), err)
	}
}

func TestAMissingPolicyFileMeansDefaults(t *testing.T) {
	ps, err := LoadPolicies(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || len(ps) != 0 {
		t.Fatalf("LoadPolicies = %v, %v", ps, err)
	}
	if got := ps.For("facts.hardware").Thresholds(); got != (Thresholds{DefaultPromoteAfter, DefaultWindow, DefaultMinRate, 360}) {
		t.Fatalf("thresholds with no policy file = %+v, want the defaults", got)
	}
}

func TestPoliciesBelowTheFloorsAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"not yaml":            "capabilities: [",
		"bad id":              "capabilities:\n  - id: Facts Hardware\n",
		"listed twice":        "capabilities:\n  - id: a\n  - id: a\n",
		"one pass":            "capabilities:\n  - id: a\n    promote_after: 1\n    window: 10\n",
		"one percent":         "capabilities:\n  - id: a\n    min_rate: 0.01\n",
		"window below streak": "capabilities:\n  - id: a\n    promote_after: 30\n    window: 10\n",
		"rate above one":      "capabilities:\n  - id: a\n    min_rate: 1.5\n",
		"window too large":    "capabilities:\n  - id: a\n    window: 5000\n",
		"negative streak":     "capabilities:\n  - id: a\n    promote_after: -1\n",
		"span under an hour":  "capabilities:\n  - id: a\n    min_span: 30m\n",
		"span not a duration": "capabilities:\n  - id: a\n    min_span: six hours\n",
		"span over 30 days":   "capabilities:\n  - id: a\n    min_span: 1000h\n",
	} {
		if _, err := LoadPolicies(writePolicy(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestForResolvesPoliciesBuiltInCode(t *testing.T) {
	ps := Policies{"a": {ID: "a", Rung: 2, Description: "d"}, "b": {ID: "b", Rung: 3, PromoteAfter: 1}}
	if got := ps.For("a"); got.Thresholds() != (Thresholds{DefaultPromoteAfter, DefaultWindow, DefaultMinRate, 360}) || got.Rung != 2 {
		t.Fatalf("a zero-threshold entry must resolve to the defaults: %+v", got)
	}
	if got := ps.For("b"); got.Thresholds() != (Thresholds{DefaultPromoteAfter, DefaultWindow, DefaultMinRate, 360}) || got.Rung != 3 {
		t.Fatalf("an entry below the floors must fall back to the defaults and keep its rung: %+v", got)
	}
	if got := (Policy{PromoteAfter: 1, Window: 1, MinRate: 0.01}).Thresholds(); got != (Thresholds{MinPromoteAfter, MinPromoteAfter, MinRateFloor, 60}) {
		t.Fatalf("Thresholds must apply the floors, got %+v", got)
	}
}
