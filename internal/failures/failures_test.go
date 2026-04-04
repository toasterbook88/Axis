package failures

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestFailuresStore(t *testing.T) {
	store := NewStore()

	scope := models.FailureScope{
		Node:     "node1",
		Workload: models.ClassLocalLLMInference,
	}

	// 1. Record Failure
	record, isNew := store.Record(models.FailureExecCrash, scope, "segfault", nil)
	if !isNew {
		t.Fatal("expected failure to be new")
	}
	if record.Count != 1 {
		t.Fatalf("expected count 1, got %d", record.Count)
	}

	// 2. NarrowestMatch
	match, ok := store.NarrowestMatch(scope)
	if !ok {
		t.Fatal("expected to find match")
	}
	if match.Class != models.FailureExecCrash {
		t.Fatalf("expected exec-crash, got %s", match.Class)
	}

	// 3. Escalate Failure
	record2, isNew2 := store.Record(models.FailureExecCrash, scope, "segfault again", nil)
	if isNew2 {
		t.Fatal("expected failure to be escalated, not new")
	}
	if record2.Count != 2 {
		t.Fatalf("expected count 2, got %d", record2.Count)
	}
	if !record2.ExpiresAt.After(record.ExpiresAt) {
		t.Fatal("expected extended expiry")
	}

	// 4. Clear Override
	if !store.ClearOverride(scope, "fixed it") {
		t.Fatal("expected to clear override")
	}
	match2, ok2 := store.NarrowestMatch(scope)
	if ok2 || match2 != nil {
		t.Fatal("expected overridden record to be ignored")
	}

	// 5. Prune
	pruned := store.Prune()
	if pruned != 1 {
		t.Fatalf("expected to prune 1 overridden/expired record, got %d", pruned)
	}

	// 6. Record Success
	store.Record(models.FailureNetwork, scope, "network down", nil)
	if !store.RecordSuccess(scope) {
		t.Fatal("expected to record success")
	}
	_, ok3 := store.NarrowestMatch(scope)
	if ok3 {
		t.Fatal("expected record to be cleared on success")
	}
}

func TestExpiryCalculation(t *testing.T) {
	if CalculateExpiry(1) != 24*time.Hour {
		t.Fatal("expected base expiry")
	}
	if CalculateExpiry(2) != 48*time.Hour {
		t.Fatal("expected doubled expiry")
	}
	if CalculateExpiry(10) != maxExpiry {
		t.Fatal("expected max expiry")
	}
}

func TestSpecificityScoreAndMatch(t *testing.T) {
	store := NewStore()

	broadScope := models.FailureScope{Node: "node1"}
	specificScope := models.FailureScope{Node: "node1", Workload: models.ClassLocalLLMInference}
	targetScope := models.FailureScope{Node: "node1", Workload: models.ClassLocalLLMInference, Tool: "ollama"}

	store.Record(models.FailureTimeout, broadScope, "broad failure", nil)
	store.Record(models.FailureExecCrash, specificScope, "specific failure", nil)

	match, ok := store.NarrowestMatch(targetScope)
	if !ok {
		t.Fatal("expected match")
	}
	if match.Class != models.FailureExecCrash {
		t.Fatalf("expected NarrowestMatch to return specific failure, got %s", match.Class)
	}
}
